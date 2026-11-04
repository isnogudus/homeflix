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
	"sort"
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
	profileSize  = "h632"
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

// Report counts what an update did: movies, then the people they list.
type Report struct {
	Matched, NotFound, Failed, Unchanged int
	People, PeopleNotFound, PeopleFailed int
	Took                                 time.Duration
}

// Changed reports whether the update stored anything new.
func (r Report) Changed() bool {
	return r.Matched+r.NotFound+r.Failed+r.People+r.PeopleNotFound+r.PeopleFailed > 0
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

// Update looks up every movie that has no current metadata, then every
// person those movies list who has not been looked up yet.
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
	var rep Report
	err := s.updateMovies(ctx, &rep)
	if err == nil && ctx.Err() == nil {
		err = s.updatePeople(ctx, &rep)
	}
	rep.Took = s.now().Sub(start)
	if err != nil {
		return rep, err
	}
	return rep, ctx.Err()
}

// parallel calls do for 0 to n-1 on s.Workers goroutines until ctx is done.
func (s *Service) parallel(ctx context.Context, n int, do func(i int)) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range max(1, s.Workers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				do(i)
			}
		}()
	}
feed:
	for i := range n {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
}

// progress reports every minute how far an update got and lets the
// catalog show what is there so far.
type progress struct {
	s     *Service
	what  string
	total int
	last  time.Time
}

func (p *progress) step(done int) {
	if p.s.Progress == nil || p.s.now().Sub(p.last) < time.Minute {
		return
	}
	p.last = p.s.now()
	if p.s.Log != nil {
		p.s.Log.Info("fetching "+p.what, "done", done, "of", p.total)
	}
	p.s.Progress()
}

func (s *Service) updateMovies(ctx context.Context, rep *Report) error {
	movies, err := s.Store.Movies(ctx)
	if err != nil {
		return err
	}
	all, err := s.Store.AllMetadata(ctx)
	if err != nil {
		return err
	}
	var todo []store.Item
	for _, it := range movies {
		md, ok := all[it.ID]
		if s.needsUpdate(it, md, ok) {
			todo = append(todo, it)
		} else {
			rep.Unchanged++
		}
	}
	if len(todo) == 0 {
		return nil
	}
	if s.Log != nil {
		s.Log.Info("fetching metadata", "movies", len(todo))
	}
	var mu sync.Mutex
	var firstErr error
	prog := progress{s: s, what: "metadata", total: len(todo), last: s.now()}
	s.parallel(ctx, len(todo), func(i int) {
		it := todo[i]
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
		defer mu.Unlock()
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
		prog.step(rep.Matched + rep.NotFound + rep.Failed)
	})
	return firstErr
}

// peopleOf returns the TMDB IDs of everyone matched movies list.
func peopleOf(all map[string]store.Metadata) map[int]bool {
	ids := map[int]bool{}
	for _, md := range all {
		if md.Status != store.MetaMatched || md.Data == "" {
			continue
		}
		var m struct {
			People []struct {
				ID int `json:"id"`
			} `json:"people"`
		}
		if json.Unmarshal([]byte(md.Data), &m) != nil {
			continue
		}
		for _, p := range m.People {
			if p.ID > 0 {
				ids[p.ID] = true
			}
		}
	}
	return ids
}

// needsPersonUpdate reports whether a person has to be looked up.
func (s *Service) needsPersonUpdate(p store.Person, ok bool) bool {
	switch {
	case !ok:
		return true
	case p.Status == store.MetaError:
		return s.now().Sub(p.FetchedAt) >= retryErrors
	case p.Status == store.MetaNotFound:
		return s.now().Sub(p.FetchedAt) >= retryNotFound
	case p.Photo != "":
		// A photo removed from the cache is fetched again.
		_, err := os.Stat(filepath.Join(s.Dir, p.Photo))
		return err != nil
	}
	return false
}

// updatePeople looks up the people of matched movies and forgets those no
// movie lists any more.
func (s *Service) updatePeople(ctx context.Context, rep *Report) error {
	all, err := s.Store.AllMetadata(ctx)
	if err != nil {
		return err
	}
	wanted := peopleOf(all)
	have, err := s.Store.AllPeople(ctx)
	if err != nil {
		return err
	}
	var gone, todo []int
	for id := range have {
		if !wanted[id] {
			gone = append(gone, id)
		}
	}
	for id := range wanted {
		if p, ok := have[id]; s.needsPersonUpdate(p, ok) {
			todo = append(todo, id)
		}
	}
	if err := s.Store.DeletePeople(ctx, gone); err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}
	sort.Ints(todo)
	if s.Log != nil {
		s.Log.Info("fetching people", "count", len(todo))
	}
	var mu sync.Mutex
	var firstErr error
	prog := progress{s: s, what: "people", total: len(todo), last: s.now()}
	s.parallel(ctx, len(todo), func(i int) {
		p, err := s.lookupPerson(ctx, todo[i])
		if ctx.Err() != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
		case p.Status == store.MetaMatched:
			rep.People++
		case p.Status == store.MetaNotFound:
			rep.PeopleNotFound++
		default:
			rep.PeopleFailed++
			if s.Log != nil {
				s.Log.Warn("person lookup failed", "tmdb_id", p.TMDbID, "err", p.Error)
			}
		}
		prog.step(rep.People + rep.PeopleNotFound + rep.PeopleFailed)
	})
	return firstErr
}

// lookupPerson fetches the details and photo of a person and stores them.
// As in lookup, problems with TMDB end up in the stored state.
func (s *Service) lookupPerson(ctx context.Context, id int) (store.Person, error) {
	p := store.Person{TMDbID: id, FetchedAt: s.now()}
	d, err := s.TMDB.Person(ctx, id, s.Language)
	if errors.Is(err, tmdb.ErrNotFound) {
		p.Status = store.MetaNotFound
		return p, s.Store.SavePerson(ctx, p)
	}
	if err == nil {
		p.Photo, err = s.image(ctx, id, "profile", d.ProfilePath, profileSize)
	}
	var data []byte
	if err == nil {
		data, err = json.Marshal(d)
	}
	if err != nil {
		if ctx.Err() != nil {
			return p, nil
		}
		p.Status, p.Error, p.Photo = store.MetaError, err.Error(), ""
		return p, s.Store.SavePerson(ctx, p)
	}
	p.Status, p.Data = store.MetaMatched, string(data)
	return p, s.Store.SavePerson(ctx, p)
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

// staleDownload is how old an unfinished download must be before it is
// removed; younger ones may belong to a running update.
const staleDownload = time.Hour

// RemoveUnusedArtwork deletes files in dir that no metadata refers to:
// artwork of movies deleted for good, artwork replaced by a newer one at
// TMDB, and downloads left over from interrupted updates. It does not
// need TMDB access, so it also runs while TMDB is switched off.
func RemoveUnusedArtwork(ctx context.Context, st *store.Store, dir string, log *slog.Logger) (int, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	all, err := st.AllMetadata(ctx)
	if err != nil {
		return 0, err
	}
	people, err := st.AllPeople(ctx)
	if err != nil {
		return 0, err
	}
	used := map[string]bool{}
	for _, md := range all {
		used[md.Poster], used[md.Backdrop] = true, true
	}
	for _, p := range people {
		used[p.Photo] = true
	}
	removed := 0
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || used[name] {
			continue
		}
		if strings.HasPrefix(name, ".download-") {
			if fi, err := de.Info(); err != nil || time.Since(fi.ModTime()) < staleDownload {
				continue
			}
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return removed, err
		}
		removed++
	}
	if removed > 0 && log != nil {
		log.Info("removed unused artwork", "files", removed)
	}
	return removed, nil
}
