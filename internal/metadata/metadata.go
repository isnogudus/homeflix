// Package metadata matches movies at TMDB and keeps their details and
// artwork locally, so each movie is looked up once.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/lockfile"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

// Retry periods for movies that could not be matched or fetched.
const (
	retryErrors   = 24 * time.Hour
	retryNotFound = 30 * 24 * time.Hour
)

// Image sizes fetched from TMDB.
const (
	posterSize   = "w780"
	backdropSize = "w1280"
)

// ErrUpdateRunning is returned when another update holds the lock.
var ErrUpdateRunning = errors.New("a metadata update is already running")

// Service keeps metadata up to date.
type Service struct {
	Store    *store.Store
	TMDB     *tmdb.Client
	Language string // e.g. "de-DE"
	Dir      string // artwork files
	Workers  int
	Log      *slog.Logger
	// Progress, if set, is called every now and then while updating, so
	// that new metadata shows up before the whole library is done.
	Progress func()
	// LockPath, if set, is locked during an update so that only one process
	// updates a data directory at a time.
	LockPath string
	Now      func() time.Time

	mu sync.Mutex
}

// Report counts what an update did.
type Report struct {
	Matched, NotFound, Failed, Unchanged int
	Took                                 time.Duration
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// MatchKey is the file name data a match is made for.
func MatchKey(it store.Item) string {
	return fmt.Sprintf("%s|%s|%d", it.IMDbID, catalog.SearchKey(it.Title), it.Year)
}

// needsUpdate reports whether a movie has to be looked up.
func (s *Service) needsUpdate(it store.Item, md store.Metadata, ok bool) bool {
	switch {
	case !ok:
		return true
	case md.Status == store.MetaManual:
		return true
	case md.Source != "manual" && md.MatchKey != MatchKey(it):
		return true
	case md.Status == store.MetaError:
		return s.now().Sub(md.FetchedAt) >= retryErrors
	case md.Status == store.MetaNotFound:
		return s.now().Sub(md.FetchedAt) >= retryNotFound
	case md.Status == store.MetaMatched:
		// Artwork removed from the cache is fetched again.
		for _, f := range []string{md.Poster, md.Backdrop} {
			if f != "" {
				if _, err := os.Stat(filepath.Join(s.Dir, f)); err != nil {
					return true
				}
			}
		}
	}
	return false
}

// Update looks up every movie that has no current metadata.
func (s *Service) Update(ctx context.Context) (Report, error) {
	if !s.mu.TryLock() {
		return Report{}, ErrUpdateRunning
	}
	defer s.mu.Unlock()
	if s.LockPath != "" {
		unlock, err := lockfile.TryLock(s.LockPath)
		if errors.Is(err, lockfile.ErrLocked) {
			return Report{}, ErrUpdateRunning
		}
		if err != nil {
			return Report{}, err
		}
		defer unlock()
	}
	start := s.now()
	movies, err := s.Store.Movies(ctx)
	if err != nil {
		return Report{}, err
	}
	all, err := s.Store.AllMetadata(ctx)
	if err != nil {
		return Report{}, err
	}
	var todo []store.Item
	var rep Report
	for _, it := range movies {
		md, ok := all[it.ID]
		if s.needsUpdate(it, md, ok) {
			todo = append(todo, it)
		} else {
			rep.Unchanged++
		}
	}
	if len(todo) == 0 {
		return rep, nil
	}
	if s.Log != nil {
		s.Log.Info("fetching metadata", "movies", len(todo))
	}
	workers := max(1, s.Workers)
	jobs := make(chan store.Item)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	lastProgress := s.now()
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				md, ok := all[it.ID]
				var prev *store.Metadata
				if ok {
					prev = &md
				}
				res, err := s.lookup(ctx, it, prev)
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				switch {
				case err != nil:
					if firstErr == nil {
						firstErr = err
					}
				case res.Status == store.MetaMatched:
					rep.Matched++
				case res.Status == store.MetaNotFound:
					rep.NotFound++
					if s.Log != nil {
						s.Log.Info("no metadata found", "title", it.Title, "year", it.Year)
					}
				default:
					rep.Failed++
					if s.Log != nil {
						s.Log.Warn("metadata lookup failed", "title", it.Title, "err", res.Error)
					}
				}
				if s.Progress != nil && s.now().Sub(lastProgress) >= time.Minute {
					lastProgress = s.now()
					done := rep.Matched + rep.NotFound + rep.Failed
					if s.Log != nil {
						s.Log.Info("fetching metadata", "done", done, "of", len(todo))
					}
					s.Progress()
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, it := range todo {
		select {
		case jobs <- it:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	rep.Took = s.now().Sub(start)
	if firstErr != nil {
		return rep, firstErr
	}
	return rep, ctx.Err()
}

// lookup matches one movie, fetches its details and artwork and stores the
// result. Problems with TMDB end up in the stored state, not in the error,
// which is for database failures.
func (s *Service) lookup(ctx context.Context, it store.Item, prev *store.Metadata) (store.Metadata, error) {
	md := store.Metadata{ItemID: it.ID, MatchKey: MatchKey(it), FetchedAt: s.now()}
	var id int
	var err error
	if prev != nil && prev.Source == "manual" && prev.TMDbID > 0 {
		md.Source, id = "manual", prev.TMDbID
	} else {
		md.Source, id, err = s.match(ctx, it)
	}
	switch {
	case errors.Is(err, tmdb.ErrNotFound):
		md.Status = store.MetaNotFound
	case err != nil:
		if ctx.Err() != nil {
			return md, nil
		}
		md.Status, md.Error = store.MetaError, err.Error()
	default:
		md.TMDbID = id
		// A title search may find another movie of the same name; when the
		// file names an IMDb ID, the result has to carry it too.
		want := ""
		if md.Source == "search" {
			want = it.IMDbID
		}
		err := s.fetch(ctx, &md, want)
		switch {
		case errors.Is(err, errOtherMovie):
			md.Status, md.TMDbID = store.MetaNotFound, 0
		case err != nil:
			if ctx.Err() != nil {
				return md, nil
			}
			md.Status, md.Error = store.MetaError, err.Error()
		default:
			md.Status = store.MetaMatched
		}
	}
	return md, s.Store.SaveMetadata(ctx, md)
}

// errOtherMovie means a search result has another IMDb ID than the file.
var errOtherMovie = errors.New("search result is another movie")

// match finds the TMDB ID of a movie: by IMDb ID if the file name has one,
// else by title and year.
func (s *Service) match(ctx context.Context, it store.Item) (string, int, error) {
	if it.IMDbID != "" {
		id, err := s.TMDB.FindByIMDb(ctx, it.IMDbID)
		if err == nil {
			return "imdb", id, nil
		}
		if !errors.Is(err, tmdb.ErrNotFound) {
			return "", 0, err
		}
	}
	// With an IMDb ID TMDB does not know, a title search is a guess about a
	// movie TMDB probably lists as television; only an exact title counts.
	exactOnly := it.IMDbID != ""
	queries := []string{it.Title}
	// "Der Astronaut - Project Hail Mary": try the parts on their own too.
	if a, b, ok := strings.Cut(it.Title, " - "); ok {
		queries = append(queries, a, b)
	}
	for _, q := range queries {
		res, err := s.TMDB.Search(ctx, q, it.Year, s.Language)
		if err != nil {
			return "", 0, err
		}
		if id, ok := pick(res, it.Title, q, it.Year, exactOnly); ok {
			return "search", id, nil
		}
		if it.Year > 0 {
			// Release years differ between countries; search without the
			// year and allow one year off.
			res, err = s.TMDB.Search(ctx, q, 0, s.Language)
			if err != nil {
				return "", 0, err
			}
			if id, ok := pick(res, it.Title, q, it.Year, exactOnly); ok {
				return "search", id, nil
			}
		}
	}
	return "", 0, tmdb.ErrNotFound
}

// pick chooses the search result that is clearly the movie, or none. A
// result counts when its title (or original title) equals the file's
// title or the query, and its year is at most one off; or when the titles
// contain each other and the year is exact. Without a year in the file
// name, or with exactOnly, only an exact title counts.
func pick(results []tmdb.SearchResult, title, query string, year int, exactOnly bool) (int, bool) {
	want := []string{catalog.SearchKey(title), catalog.SearchKey(query)}
	best, bestScore := 0, 0
	for _, r := range results {
		exact, partial := false, false
		for _, t := range []string{catalog.SearchKey(r.Title), catalog.SearchKey(r.OriginalTitle)} {
			if t == "" {
				continue
			}
			for _, w := range want {
				switch {
				case w == t:
					exact = true
				case containsWords(t, w) || containsWords(w, t):
					partial = true
				}
			}
		}
		diff := year - r.Year()
		if diff < 0 {
			diff = -diff
		}
		score := 0
		switch {
		case year == 0 && exact:
			score = 2
		case exact && diff == 0:
			score = 4
		case exact && diff == 1:
			score = 3
		case partial && diff == 0 && !exactOnly:
			score = 2
		}
		if score > bestScore {
			best, bestScore = r.ID, score
		}
	}
	return best, bestScore > 0
}

// containsWords reports whether the words of sub appear in s as whole
// words, so "tron ares 3d" contains "tron ares" but "heathers" does not
// contain "heat". Search keys are lowercase words separated by spaces.
func containsWords(s, sub string) bool {
	if len(sub) < 4 {
		return false
	}
	return strings.Contains(" "+s+" ", " "+sub+" ")
}

// fetch loads the details and artwork of md.TMDbID into md. If imdbID is
// set and TMDB knows another IMDb ID for the movie, it returns
// errOtherMovie before fetching artwork.
func (s *Service) fetch(ctx context.Context, md *store.Metadata, imdbID string) error {
	m, err := s.TMDB.Movie(ctx, md.TMDbID, s.Language)
	if err != nil {
		return err
	}
	if imdbID != "" && m.IMDbID != "" && !strings.EqualFold(m.IMDbID, imdbID) {
		return errOtherMovie
	}
	if (m.Overview == "" || m.Tagline == "" || len(m.Genres) == 0) && s.Language != "en-US" {
		if en, err := s.TMDB.Movie(ctx, md.TMDbID, "en-US"); err == nil {
			if m.Overview == "" {
				m.Overview = en.Overview
			}
			if m.Tagline == "" {
				m.Tagline = en.Tagline
			}
			if len(m.Genres) == 0 {
				m.Genres = en.Genres
			}
			if m.PosterPath == "" {
				m.PosterPath = en.PosterPath
			}
		}
	}
	if md.Poster, err = s.image(ctx, m.ID, "poster", m.PosterPath, posterSize); err != nil {
		return err
	}
	if md.Backdrop, err = s.image(ctx, m.ID, "backdrop", m.BackdropPath, backdropSize); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	md.Data = string(data)
	return nil
}

// image stores a TMDB image as "<tmdb id>-<kind>-<tmdb file name>" in the
// metadata directory and returns that name; an image already there is
// not fetched again.
func (s *Service) image(ctx context.Context, id int, kind, tmdbPath, size string) (string, error) {
	if tmdbPath == "" {
		return "", nil
	}
	name := strconv.Itoa(id) + "-" + kind + "-" + path.Base(tmdbPath)
	full := filepath.Join(s.Dir, name)
	if _, err := os.Stat(full); err == nil {
		return name, nil
	}
	data, err := s.TMDB.Image(ctx, tmdbPath, size)
	if err != nil {
		return "", fmt.Errorf("%s image: %w", kind, err)
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.Dir, ".download-*")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		return "", errors.Join(werr, cerr)
	}
	return name, os.Rename(f.Name(), full)
}

// Match sets the TMDB ID of a movie by hand and fetches its metadata.
func (s *Service) Match(ctx context.Context, it store.Item, tmdbID int) (store.Metadata, error) {
	md := store.Metadata{ItemID: it.ID, Source: "manual", MatchKey: MatchKey(it), TMDbID: tmdbID, FetchedAt: s.now()}
	if err := s.fetch(ctx, &md, ""); err != nil {
		return md, err
	}
	md.Status = store.MetaMatched
	return md, s.Store.SaveMetadata(ctx, md)
}
