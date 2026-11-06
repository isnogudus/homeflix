package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"

	"homeflix/internal/config"
	"homeflix/internal/keyframes"
	"homeflix/internal/library"
	"homeflix/internal/probe"
	"homeflix/internal/store"
)

func newScanner(cfg config.Config, db *store.Store, log *slog.Logger) *library.Scanner {
	return &library.Scanner{
		Store:        db,
		Libraries:    cfg.Libraries,
		Prober:       probe.FFprobe{Path: cfg.FFprobe},
		Workers:      cfg.ProbeWorkers,
		SortArticles: cfg.SortArticles,
		LockPath:     filepath.Join(cfg.DataDir, "scan.lock"),
		Log:          log,
	}
}

func newIndexer(cfg config.Config, db *store.Store, log *slog.Logger) *keyframes.Indexer {
	return &keyframes.Indexer{Store: db, Workers: cfg.ProbeWorkers, Log: log,
		Finder: keyframes.Finder{FFprobe: cfg.FFprobe, Scans: make(chan struct{}, 1)}}
}

func scanCommand(args []string, cio cliIO) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(cio.stderr)
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	reprobe := fs.Bool("reprobe", false, "probe every file again, not only new and changed ones")
	reindex := fs.Bool("reindex", false, "find the keyframes of every file again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	level := cfg.SlogLevel()
	log := slog.New(slog.NewTextHandler(cio.stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	if *reprobe {
		if err := db.ResetProbes(ctx); err != nil {
			return err
		}
	}
	if *reindex {
		if err := db.ResetKeyframes(ctx); err != nil {
			return err
		}
	}
	rep, err := newScanner(cfg, db, log).Scan(ctx)
	printReport(cio.stdout, rep)
	if err != nil {
		return err
	}
	ix := newIndexer(cfg, db, log)
	ix.Shutdown = ctx
	krep, err := ix.IndexAll(ctx)
	fmt.Fprintf(cio.stdout, "keyframes: %d from the container index, %d with ffprobe, %d failed, took %s\n",
		krep.Indexed, krep.Scanned, krep.Failed, krep.Took.Round(1e7))
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if rep.Changed() {
		return bumpCatalog(ctx, db)
	}
	return nil
}

func printReport(w io.Writer, rep library.Report) {
	for _, l := range rep.Libraries {
		fmt.Fprintf(w, "%s: %d movies (%d added, %d updated, %d moved, %d returned, %d missing); files %d added, %d changed, %d removed\n",
			l.Name, l.Movies, l.Added, l.Updated, l.Moved, l.Returned, l.Missing, l.FilesAdded, l.FilesChanged, l.FilesRemoved)
		for _, p := range l.UnavailablePaths {
			fmt.Fprintf(w, "  unavailable: %s\n", p)
		}
	}
	fmt.Fprintf(w, "purged %d, probed %d, probe failures %d, took %s\n",
		rep.Purged, rep.Probed, rep.ProbeFailed, rep.Took.Round(1e7))
}

func statsCommand(args []string, cio cliIO) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(cio.stderr)
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	s, err := library.CollectStats(ctx, db)
	if err != nil {
		return err
	}
	w := cio.stdout
	fmt.Fprintf(w, "%d movies, %d extras, %d files (%d not yet probed, %d failed)\n",
		s.Movies, s.Extras, s.Files, s.Unprobed, len(s.Failed))
	fmt.Fprintf(w, "h264 >= 1900 px with more than 4 reference frames: %d (unknown: %d)\n",
		s.H264ManyRefFrames, s.H264UnknownRefFrames)
	for _, sec := range []struct {
		title string
		c     library.Counter
	}{
		{"containers", s.Containers},
		{"video", s.VideoCodecs},
		{"resolution", s.Resolutions},
		{"dynamic range", s.HDR},
		{"audio", s.AudioCodecs},
		{"h264 reference frames at >= 1900 px", s.H264RefFrames},
		{"audio tracks per file", s.AudioTracks},
		{"subtitles", s.SubtitleCodecs},
	} {
		fmt.Fprintf(w, "\n%s:\n", sec.title)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', tabwriter.AlignRight)
		for _, k := range sec.c.Sorted() {
			fmt.Fprintf(tw, "\t%d\t  %s\n", sec.c[k], k)
		}
		tw.Flush()
	}
	if len(s.NoYear) > 0 {
		fmt.Fprintf(w, "\nmovies without a year:\n")
		for _, t := range s.NoYear {
			fmt.Fprintf(w, "  %s\n", t)
		}
	}
	if len(s.Failed) > 0 {
		fmt.Fprintf(w, "\nprobe failures:\n")
		for _, f := range s.Failed {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	return nil
}

func backupCommand(args []string, cio cliIO) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(cio.stderr)
	fs.Usage = func() {
		fmt.Fprint(cio.stderr, `Usage: homeflix backup [-config path] <file>

Writes a consistent copy of the database to file, also while the server
runs. Keep it together with the metadata directory (TMDB artwork); the
images directory is a cache and need not be kept.
`)
	}
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		fs.Usage()
		return errors.New("backup: want one file name")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Backup(ctx, rest[0]); err != nil {
		return err
	}
	fmt.Fprintf(cio.stdout, "database written to %s\nkeep it with %s; %s need not be kept\n",
		rest[0], metadataDir(cfg), filepath.Join(cfg.DataDir, "images"))
	return nil
}
