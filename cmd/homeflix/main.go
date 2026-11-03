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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/config"
	"homeflix/internal/images"
	"homeflix/internal/jellyfin"
	"homeflix/internal/library"
	"homeflix/internal/media"
	"homeflix/internal/store"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const defaultConfigPath = "/usr/local/etc/homeflix.toml"

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
		return serve(args[1:], cio.stderr)
	case "scan":
		return scanCommand(args[1:], cio)
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

func serve(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	level, _ := cfg.SlogLevel()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

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
	cat := &catalog.Catalog{Store: db, Locale: media.Locale(cfg.DisplayLanguage), Log: log}
	if err := cat.Reload(ctx); err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}
	imgs := &images.Service{FFmpeg: cfg.FFmpeg, Dir: filepath.Join(cfg.DataDir, "images"), Workers: cfg.ImageWorkers, Log: log}

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
			imgs.Warm(ctx, cat.Snapshot().AllMovies())
		}()
	}
	scanner := newScanner(cfg, db, log)
	scanTrigger := make(chan struct{}, 1)
	background.Add(1)
	go func() {
		defer background.Done()
		runScans(ctx, scanner, cfg.ScanInterval.Duration, scanTrigger, log, func(rep library.Report) {
			if rep.Changed() {
				if err := cat.Reload(ctx); err != nil && ctx.Err() == nil {
					log.Error("reload catalog", "err", err)
				}
			}
			warm()
		})
	}()

	api := &jellyfin.Server{
		ServerID:         serverID,
		ServerName:       cfg.ServerName,
		Store:            db,
		Catalog:          cat,
		Images:           imgs,
		Log:              log,
		FailedLoginDelay: time.Second,
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
