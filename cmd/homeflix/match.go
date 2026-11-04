package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/config"
	"homeflix/internal/metadata"
	"homeflix/internal/outbound"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

// catalogVersionKey is bumped by commands that change what clients see, so
// a running server reloads its catalog.
const catalogVersionKey = "catalog_version"

func bumpCatalog(ctx context.Context, db *store.Store) error {
	return db.SetSetting(ctx, catalogVersionKey, strconv.FormatInt(time.Now().UnixNano(), 10))
}

func metadataDir(cfg config.Config) string { return filepath.Join(cfg.DataDir, "metadata") }

// newMetadata returns the metadata service, or nil when TMDB is not
// configured. Only then is a network client created, for TMDB's hosts.
func newMetadata(cfg config.Config, db *store.Store, log *slog.Logger) *metadata.Service {
	if cfg.TMDB == nil {
		return nil
	}
	return &metadata.Service{
		Store:    db,
		TMDB:     &tmdb.Client{HTTP: outbound.New(log, tmdb.Hosts...), Token: cfg.TMDB.Token},
		Language: cfg.TMDB.Language,
		Dir:      metadataDir(cfg),
		Workers:  4,
		LockPath: filepath.Join(cfg.DataDir, "metadata.lock"),
		Log:      log,
		// A refresh is due once the data is this old.
		RefreshAfter: cfg.TMDB.RefreshInterval,
	}
}

const matchUsage = `Usage: homeflix match <subcommand> [flags] [args]

Subcommands:
  list                  movies without metadata, and why
  set <movie> <tmdb-id> match a movie by hand; kept across renames
  clear <movie>         forget the match and look the movie up again
  update                look up every movie without current metadata now

<movie> is an item ID, a title, or "Title (Year)". The TMDB ID is the
number in a TMDB address such as https://www.themoviedb.org/movie/329865.

Flags:
  -config path    configuration file (default ` + defaultConfigPath + `)
`

func matchCommand(args []string, cio cliIO) error {
	if len(args) == 0 {
		fmt.Fprint(cio.stderr, matchUsage)
		return errors.New("missing match subcommand")
	}
	sub := args[0]
	fs := flag.NewFlagSet("match "+sub, flag.ContinueOnError)
	fs.SetOutput(cio.stderr)
	fs.Usage = func() { fmt.Fprint(cio.stderr, matchUsage) }
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	rest, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	want := map[string]int{"list": 0, "set": 2, "clear": 1, "update": 0}
	n, ok := want[sub]
	if !ok {
		fmt.Fprint(cio.stderr, matchUsage)
		return fmt.Errorf("unknown match subcommand %q", sub)
	}
	if len(rest) != n {
		fmt.Fprint(cio.stderr, matchUsage)
		return fmt.Errorf("match %s: want %d argument(s), got %d", sub, n, len(rest))
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(cio.stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	if sub == "list" {
		return listUnmatched(ctx, db, cio)
	}
	svc := newMetadata(cfg, db, log)
	if svc == nil {
		return errors.New("TMDB is not configured; add a tmdb block to the configuration")
	}
	switch sub {
	case "update":
		rep, err := svc.Update(ctx)
		fmt.Fprintf(cio.stdout, "movies: matched %d, not found %d, failed %d, unchanged %d\n",
			rep.Matched, rep.NotFound, rep.Failed, rep.Unchanged)
		fmt.Fprintf(cio.stdout, "people: fetched %d, not found %d, failed %d\n",
			rep.People, rep.PeopleNotFound, rep.PeopleFailed)
		fmt.Fprintf(cio.stdout, "collections: fetched %d, not found %d, failed %d; took %s\n",
			rep.Collections, rep.CollectionsNotFound, rep.CollectionsFailed, rep.Took.Round(time.Second))
		if err != nil {
			return err
		}
	case "set", "clear":
		it, err := findMovie(ctx, db, rest[0])
		if err != nil {
			return err
		}
		var md store.Metadata
		if sub == "set" {
			id, err := strconv.Atoi(rest[1])
			if err != nil || id <= 0 {
				return fmt.Errorf("TMDB ID %q is not a positive number", rest[1])
			}
			if md, err = svc.Match(ctx, it, id); err != nil {
				return err
			}
		} else {
			if err := db.DeleteMetadata(ctx, it.ID); err != nil {
				return err
			}
			if _, err := svc.Update(ctx); err != nil {
				return err
			}
			if md, err = db.GetMetadata(ctx, it.ID); err != nil {
				return err
			}
		}
		printMatch(cio, it, md)
	}
	return bumpCatalog(ctx, db)
}

func printMatch(cio cliIO, it store.Item, md store.Metadata) {
	switch md.Status {
	case store.MetaMatched:
		fmt.Fprintf(cio.stdout, "%s (%d): TMDB %d (%s)\n", it.Title, it.Year, md.TMDbID, md.Source)
	case store.MetaNotFound:
		fmt.Fprintf(cio.stdout, "%s (%d): not found at TMDB\n", it.Title, it.Year)
	default:
		fmt.Fprintf(cio.stdout, "%s (%d): %s %s\n", it.Title, it.Year, md.Status, md.Error)
	}
}

var titleYear = regexp.MustCompile(`^(.*\S)\s*\((\d{4})\)$`)

// findMovie resolves an item ID, a title or "Title (Year)" to one movie.
func findMovie(ctx context.Context, db *store.Store, query string) (store.Item, error) {
	movies, err := db.Movies(ctx)
	if err != nil {
		return store.Item{}, err
	}
	all, err := db.AllMetadata(ctx)
	if err != nil {
		return store.Item{}, err
	}
	id := strings.ToLower(strings.ReplaceAll(query, "-", ""))
	title, year := query, 0
	if m := titleYear.FindStringSubmatch(query); m != nil {
		title, year = m[1], atoi(m[2])
	}
	key := catalog.SearchKey(title)
	var found []store.Item
	for _, it := range movies {
		if it.ID == id {
			return it, nil
		}
		names := []string{it.Title}
		if md, ok := all[it.ID]; ok && md.Data != "" {
			names = append(names, tmdbTitle(md.Data))
		}
		for _, n := range names {
			if catalog.SearchKey(n) == key && (year == 0 || it.Year == year) {
				found = append(found, it)
				break
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return store.Item{}, fmt.Errorf("no movie %q", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d movies; give the ID:", query, len(found))
	for _, it := range found {
		fmt.Fprintf(&b, "\n  %s  %s (%d)  %s", it.ID, it.Title, it.Year, it.Path)
	}
	return store.Item{}, errors.New(b.String())
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func tmdbTitle(data string) string {
	var m tmdb.Movie
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return ""
	}
	return m.Title
}

func listUnmatched(ctx context.Context, db *store.Store, cio cliIO) error {
	movies, err := db.Movies(ctx)
	if err != nil {
		return err
	}
	all, err := db.AllMetadata(ctx)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	tw := tabwriter.NewWriter(cio.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tYEAR\tSTATE")
	for _, it := range movies {
		md, ok := all[it.ID]
		state := "not looked up"
		if ok {
			state = md.Status
			if md.Error != "" {
				state += ": " + md.Error
			}
			counts[md.Status]++
		} else {
			counts["pending"]++
		}
		if ok && md.Status == store.MetaMatched {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", it.ID, it.Title, it.Year, state)
	}
	tw.Flush()
	fmt.Fprintf(cio.stdout, "\n%d movies: %d matched, %d not found, %d failed, %d not looked up yet\n",
		len(movies), counts[store.MetaMatched], counts[store.MetaNotFound], counts[store.MetaError], counts["pending"])
	return nil
}
