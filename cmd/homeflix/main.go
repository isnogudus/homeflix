// Command homeflix is a media server that speaks the Jellyfin client API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/config"
	"homeflix/internal/discovery"
	"homeflix/internal/images"
	"homeflix/internal/jellyfin"
	"homeflix/internal/library"
	"homeflix/internal/media"
	"homeflix/internal/metadata"
	"homeflix/internal/store"
	"homeflix/internal/subtitles"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const defaultConfigPath = "/usr/local/etc/homeflix.conf"

func main() {
	cio := cliIO{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	if err := run(os.Args[1:], cio); err != nil {
		fmt.Fprintln(os.Stderr, "homeflix:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: homeflix <command> [flags]

Commands:
  serve     run the server
  scan      scan the libraries once and probe new files
  match     look up movies at TMDB, list and correct matches
  backup    write a consistent copy of the database
  stats     summarise containers and codecs of the libraries
  user      manage users (add, passwd, delete, list)
  version   print the version

Run "homeflix <command> -h" for the flags of a command.
`)
}

func run(args []string, cio cliIO) error {
	if len(args) == 0 {
		usage(cio.stderr)
		return errors.New("missing command")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], cio.stdout, cio.stderr)
	case "scan":
		return scanCommand(args[1:], cio)
	case "match":
		return matchCommand(args[1:], cio)
	case "backup":
		return backupCommand(args[1:], cio)
	case "stats":
		return statsCommand(args[1:], cio)
	case "user":
		return userCommand(args[1:], cio)
	case "version":
		fmt.Fprintf(cio.stdout, "homeflix %s (%s, %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	case "help", "-h", "-help", "--help":
		usage(cio.stdout)
		return nil
	default:
		usage(cio.stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	checkOnly := fs.Bool("n", false, "check the configuration only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *checkOnly {
		for _, w := range cfg.Warnings {
			fmt.Fprintln(stderr, "warning:", w)
		}
		fmt.Fprintln(stdout, "configuration OK")
		return nil
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	for _, w := range cfg.Warnings {
		log.Warn("configuration", "warning", w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	serverID, err := db.ServerID(ctx)
	if err != nil {
		return fmt.Errorf("server ID: %w", err)
	}

	if _, err := exec.LookPath(cfg.FFprobe); err != nil {
		log.Warn("ffprobe not found; new files cannot be probed", "ffprobe", cfg.FFprobe, "err", err)
	}
	if _, err := exec.LookPath(cfg.FFmpeg); err != nil {
		log.Warn("ffmpeg not found; no images can be made", "ffmpeg", cfg.FFmpeg, "err", err)
	}
	if len(cfg.Libraries) == 0 {
		log.Warn("no libraries configured")
	}
	cat := &catalog.Catalog{Store: db, Locale: media.Locale(cfg.DisplayLanguage), Articles: cfg.SortArticles,
		MetadataDir: metadataDir(cfg), Log: log}
	if err := cat.Reload(ctx); err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}
	reload := func() {
		if err := cat.Reload(ctx); err != nil && ctx.Err() == nil {
			log.Error("reload catalog", "err", err)
		}
	}
	meta := newMetadata(cfg, db, log)
	if meta != nil {
		meta.Progress = reload
		log.Info("TMDB metadata enabled", "language", cfg.TMDB.Language, "token_file", cfg.TMDB.TokenFile)
	} else {
		log.Info("TMDB metadata off; homeflix makes no outbound connections")
	}
	imgs := &images.Service{FFmpeg: cfg.FFmpeg, Dir: filepath.Join(cfg.DataDir, "images"), Workers: cfg.ImageWorkers, Log: log}
	subs := &subtitles.Service{FFmpeg: cfg.FFmpeg, Dir: filepath.Join(cfg.DataDir, "subtitles"), Log: log}

	var background sync.WaitGroup
	defer background.Wait()
	// Runs before the Wait above, so a failing listener also ends the
	// background work.
	defer stop()
	var warming atomic.Bool
	warm := func() {
		if !warming.CompareAndSwap(false, true) {
			return
		}
		background.Add(1)
		go func() {
			defer background.Done()
			defer warming.Store(false)
			snap := cat.Snapshot()
			movies := snap.AllMovies()
			subs.RemoveUnused(movies)
			imgs.Warm(ctx, snap)
		}()
	}
	scanner := newScanner(cfg, db, log)
	scanTrigger := make(chan struct{}, 1)
	background.Add(1)
	go func() {
		defer background.Done()
		runScans(ctx, scanner, cfg.ScanInterval, scanTrigger, log, func(rep library.Report) {
			if rep.Changed() {
				reload()
			}
			if meta != nil {
				mrep, err := meta.Update(ctx)
				switch {
				case errors.Is(err, metadata.ErrUpdateRunning):
				case err != nil && ctx.Err() == nil:
					log.Error("metadata update failed", "err", err)
				}
				if mrep.Changed() {
					log.Info("metadata updated", "matched", mrep.Matched, "not_found", mrep.NotFound,
						"failed", mrep.Failed, "unchanged", mrep.Unchanged, "people", mrep.People,
						"people_not_found", mrep.PeopleNotFound, "people_failed", mrep.PeopleFailed,
						"collections", mrep.Collections, "collections_failed", mrep.CollectionsFailed,
						"took", mrep.Took.Round(time.Second))
					reload()
				}
			}
			if _, err := metadata.RemoveUnusedArtwork(ctx, db, metadataDir(cfg), log); err != nil && ctx.Err() == nil {
				log.Error("remove unused artwork", "err", err)
			}
			warm()
		})
	}()
	if cfg.Discovery {
		startDiscovery(ctx, &background, cfg, serverID, log)
	}
	// Commands such as "homeflix match" change the database while the
	// server runs; they bump a version the server watches.
	background.Add(1)
	go func() {
		defer background.Done()
		watchCatalogVersion(ctx, db, reload, log)
	}()

	api := &jellyfin.Server{
		ServerID:         serverID,
		ServerName:       cfg.ServerName,
		Store:            db,
		Catalog:          cat,
		Images:           imgs,
		Subtitles:        subs,
		Log:              log,
		FailedLoginDelay: time.Second,
		Shutdown:         ctx,
		RequestScan: func() {
			select {
			case scanTrigger <- struct{}{}:
			default:
			}
		},
	}
	httpServer := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("homeflix started",
		"version", version,
		"listen", ln.Addr().String(),
		"server_name", cfg.ServerName,
		"server_id", serverID,
		"data_dir", cfg.DataDir,
		"config", *configPath)

	errc := make(chan error, 1)
	go func() { errc <- httpServer.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}

// startDiscovery answers "who is JellyfinServer?" broadcasts, unless the
// API listens on loopback only, where nobody else could reach it.
func startDiscovery(ctx context.Context, wg *sync.WaitGroup, cfg config.Config, serverID string, log *slog.Logger) {
	host, portText, _ := net.SplitHostPort(cfg.Listen)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = ""
	}
	port, _ := strconv.Atoi(portText)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: discovery.Port})
	if err != nil {
		log.Warn("discovery off: cannot listen", "port", discovery.Port, "err", err)
		return
	}
	r := &discovery.Responder{ServerID: serverID, ServerName: cfg.ServerName, Host: host, Port: port, Log: log}
	log.Info("answering discovery requests", "port", discovery.Port)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := r.Serve(ctx, conn); err != nil {
			log.Error("discovery stopped", "err", err)
		}
	}()
}

// watchCatalogVersion reloads the catalog when another process bumped the
// catalog version, checking every 30 seconds.
func watchCatalogVersion(ctx context.Context, db *store.Store, reload func(), log *slog.Logger) {
	last, _, _ := db.Setting(ctx, catalogVersionKey)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			v, _, err := db.Setting(ctx, catalogVersionKey)
			if err != nil || v == last {
				continue
			}
			last = v
			log.Info("catalog changed by another process")
			reload()
		}
	}
}

// runScans scans at startup, then every interval (if positive) and
// whenever trigger fires, until ctx is done. done is called after every
// completed scan.
func runScans(ctx context.Context, sc *library.Scanner, interval time.Duration, trigger <-chan struct{},
	log *slog.Logger, done func(library.Report)) {
	scan := func(reason string) {
		log.Info("library scan started", "reason", reason)
		rep, err := sc.Scan(ctx)
		switch {
		case ctx.Err() != nil:
			log.Info("library scan interrupted")
			return
		case errors.Is(err, library.ErrScanRunning):
			log.Info("library scan skipped: another scan is running")
			return
		case err != nil:
			log.Error("library scan failed", "err", err)
		}
		for _, l := range rep.Libraries {
			log.Info("library scanned", "library", l.Name, "movies", l.Movies, "added", l.Added,
				"updated", l.Updated, "moved", l.Moved, "returned", l.Returned, "missing", l.Missing,
				"files_added", l.FilesAdded, "files_changed", l.FilesChanged, "files_removed", l.FilesRemoved,
				"unavailable", len(l.UnavailablePaths))
		}
		log.Info("library scan finished", "purged", rep.Purged, "probed", rep.Probed,
			"probe_failures", rep.ProbeFailed, "took", rep.Took.Round(time.Millisecond))
		done(rep)
	}
	scan("startup")
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			scan("interval")
		case <-trigger:
			scan("requested")
		}
	}
}
