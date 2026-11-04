// Package metadata matches movies at TMDB and keeps their details and
// artwork locally, so each movie is looked up once.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
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
	// RefreshAfter is how old matched metadata gets before it is fetched
	// again; 0 keeps it as first fetched.
	RefreshAfter time.Duration
	Now          func() time.Time

	mu sync.Mutex
}

// Report counts what an update did: movies, then the people they list and
// the film series they belong to.
type Report struct {
	Matched, NotFound, Failed, Unchanged                int
	People, PeopleNotFound, PeopleFailed                int
	Collections, CollectionsNotFound, CollectionsFailed int
	Took                                                time.Duration
}

// Changed reports whether the update stored anything new.
func (r Report) Changed() bool {
	return r.Matched+r.NotFound+r.Failed+r.People+r.PeopleNotFound+r.PeopleFailed+
		r.Collections+r.CollectionsNotFound+r.CollectionsFailed > 0
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// refreshDue reports whether metadata fetched at a time is due again. Each
// key waits up to a third of RefreshAfter longer, so that what was fetched
// in one run is refreshed over weeks instead of in one burst.
func (s *Service) refreshDue(key string, fetched time.Time) bool {
	if s.RefreshAfter <= 0 {
		return false
	}
	h := fnv.New64a()
	h.Write([]byte(key))
	spread := time.Duration(h.Sum64() % uint64(s.RefreshAfter/3+1))
	return s.now().Sub(fetched) >= s.RefreshAfter+spread
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
		// Data of an older version lacks fields; it is fetched again.
		var v struct {
			Version int `json:"v"`
		}
		if json.Unmarshal([]byte(md.Data), &v) != nil || v.Version < tmdb.DataVersion {
			return true
		}
		if s.refreshDue("movie:"+md.ItemID, md.FetchedAt) {
			return true
		}
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
// person and film series those movies list that has not been looked up yet.
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

// dataOf decodes the stored movie data of matched metadata.
func dataOf(all map[string]store.Metadata) []tmdb.Movie {
	var out []tmdb.Movie
	for _, md := range all {
		if md.Status != store.MetaMatched || md.Data == "" {
			continue
		}
		var m tmdb.Movie
		if json.Unmarshal([]byte(md.Data), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// peopleOf returns the TMDB IDs of everyone matched movies list.
func peopleOf(movies []tmdb.Movie) map[int]bool {
	ids := map[int]bool{}
	for _, m := range movies {
		for _, p := range m.People {
			if p.ID > 0 {
				ids[p.ID] = true
			}
		}
	}
	return ids
}

// collectionsOf returns the TMDB IDs of the film series at least two
// matched movies belong to; a series of one is not shown.
func collectionsOf(movies []tmdb.Movie) map[int]bool {
	n := map[int]int{}
	for _, m := range movies {
		if m.Collection != nil {
			n[m.Collection.ID]++
		}
	}
	ids := map[int]bool{}
	for id, c := range n {
		if c >= 2 {
			ids[id] = true
		}
	}
	return ids
}

// lookupState is what updateSet needs to know of a stored person or
// collection.
type lookupState struct {
	status  string
	fetched time.Time
	files   []string
}

// needsLookup reports whether a person or collection has to be looked up.
func (s *Service) needsLookup(key string, st lookupState, ok bool) bool {
	switch {
	case !ok:
		return true
	case st.status == store.MetaError:
		return s.now().Sub(st.fetched) >= retryErrors
	case st.status == store.MetaNotFound:
		return s.now().Sub(st.fetched) >= retryNotFound
	case s.refreshDue(key, st.fetched):
		return true
	}
	// Artwork removed from the cache is fetched again.
	for _, f := range st.files {
		if f == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.Dir, f)); err != nil {
			return true
		}
	}
	return false
}

// setCounts counts the lookups of one kind.
type setCounts struct{ fetched, notFound, failed int }

// updateSet looks up the wanted people or collections that need it and
// deletes the stored ones no longer wanted. lookup stores one and returns
// its state and error message.
func (s *Service) updateSet(ctx context.Context, what string, wanted map[int]bool, have map[int]lookupState,
	del func(context.Context, []int) error,
	lookup func(context.Context, int) (status, msg string, err error)) (setCounts, error) {
	var c setCounts
	var gone, todo []int
	for id := range have {
		if !wanted[id] {
			gone = append(gone, id)
		}
	}
	for id := range wanted {
		if st, ok := have[id]; s.needsLookup(what+":"+strconv.Itoa(id), st, ok) {
			todo = append(todo, id)
		}
	}
	if err := del(ctx, gone); err != nil {
		return c, err
	}
	if len(todo) == 0 {
		return c, nil
	}
	sort.Ints(todo)
	if s.Log != nil {
		s.Log.Info("fetching "+what, "count", len(todo))
	}
	var mu sync.Mutex
	var firstErr error
	prog := progress{s: s, what: what, total: len(todo), last: s.now()}
	s.parallel(ctx, len(todo), func(i int) {
		status, msg, err := lookup(ctx, todo[i])
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
		case status == store.MetaMatched:
			c.fetched++
		case status == store.MetaNotFound:
			c.notFound++
		default:
			c.failed++
			if s.Log != nil {
				s.Log.Warn(what+" lookup failed", "tmdb_id", todo[i], "err", msg)
			}
		}
		prog.step(c.fetched + c.notFound + c.failed)
	})
	return c, firstErr
}

// updatePeople looks up the people and film series of matched movies and
// forgets those no movie lists any more.
func (s *Service) updatePeople(ctx context.Context, rep *Report) error {
	all, err := s.Store.AllMetadata(ctx)
	if err != nil {
		return err
	}
	movies := dataOf(all)

	people, err := s.Store.AllPeople(ctx)
	if err != nil {
		return err
	}
	have := map[int]lookupState{}
	for id, p := range people {
		have[id] = lookupState{p.Status, p.FetchedAt, []string{p.Photo}}
	}
	c, err := s.updateSet(ctx, "people", peopleOf(movies), have, s.Store.DeletePeople,
		func(ctx context.Context, id int) (string, string, error) {
			var prev *store.Person
			if p, ok := people[id]; ok {
				prev = &p
			}
			p, err := s.lookupPerson(ctx, id, prev)
			return p.Status, p.Error, err
		})
	rep.People, rep.PeopleNotFound, rep.PeopleFailed = c.fetched, c.notFound, c.failed
	if err != nil || ctx.Err() != nil {
		return err
	}

	cols, err := s.Store.AllCollections(ctx)
	if err != nil {
		return err
	}
	have = map[int]lookupState{}
	for id, col := range cols {
		have[id] = lookupState{col.Status, col.FetchedAt, []string{col.Poster, col.Backdrop}}
	}
	c, err = s.updateSet(ctx, "collections", collectionsOf(movies), have, s.Store.DeleteCollections,
		func(ctx context.Context, id int) (string, string, error) {
			var prev *store.Collection
			if c, ok := cols[id]; ok {
				prev = &c
			}
			col, err := s.lookupCollection(ctx, id, prev)
			return col.Status, col.Error, err
		})
	rep.Collections, rep.CollectionsNotFound, rep.CollectionsFailed = c.fetched, c.notFound, c.failed
	return err
}

// lookupPerson fetches the details and photo of a person and stores them.
// As in lookup, problems with TMDB end up in the stored state; a refresh
// that fails keeps what prev holds.
func (s *Service) lookupPerson(ctx context.Context, id int, prev *store.Person) (store.Person, error) {
	p := store.Person{TMDbID: id, FetchedAt: s.now()}
	d, err := s.TMDB.Person(ctx, id, s.Language)
	if err == nil {
		p.Photo, err = s.image(ctx, id, "profile", d.ProfilePath, profileSize)
	}
	var data []byte
	if err == nil {
		data, err = json.Marshal(d)
	}
	switch {
	case err == nil:
		p.Status, p.Data = store.MetaMatched, string(data)
	case ctx.Err() != nil:
		return p, nil
	case errors.Is(err, tmdb.ErrNotFound) && d.ID == 0:
		p.Status = store.MetaNotFound
	default:
		p.Status, p.Error, p.Photo = store.MetaError, err.Error(), ""
	}
	if p.Status != store.MetaMatched && prev != nil && prev.Status == store.MetaMatched {
		kept := *prev
		kept.FetchedAt = p.FetchedAt
		p.Status, p.Error = store.MetaError, "refresh: "+errorText(err)
		return p, s.Store.SavePerson(ctx, kept)
	}
	return p, s.Store.SavePerson(ctx, p)
}

// lookupCollection fetches the details and artwork of a film series and
// stores them, as lookupPerson does for a person.
func (s *Service) lookupCollection(ctx context.Context, id int, prev *store.Collection) (store.Collection, error) {
	c := store.Collection{TMDbID: id, FetchedAt: s.now()}
	d, err := s.TMDB.Collection(ctx, id, s.Language)
	if err == nil {
		c.Poster, err = s.image(ctx, id, "collection-poster", d.PosterPath, posterSize)
	}
	if err == nil {
		c.Backdrop, err = s.image(ctx, id, "collection-backdrop", d.BackdropPath, backdropSize)
	}
	var data []byte
	if err == nil {
		data, err = json.Marshal(d)
	}
	switch {
	case err == nil:
		c.Status, c.Data = store.MetaMatched, string(data)
	case ctx.Err() != nil:
		return c, nil
	case errors.Is(err, tmdb.ErrNotFound) && d.ID == 0:
		c.Status = store.MetaNotFound
	default:
		c.Status, c.Error, c.Poster, c.Backdrop = store.MetaError, err.Error(), "", ""
	}
	if c.Status != store.MetaMatched && prev != nil && prev.Status == store.MetaMatched {
		kept := *prev
		kept.FetchedAt = c.FetchedAt
		c.Status, c.Error = store.MetaError, "refresh: "+errorText(err)
		return c, s.Store.SaveCollection(ctx, kept)
	}
	return c, s.Store.SaveCollection(ctx, c)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// lookup matches one movie, fetches its details and artwork and stores the
// result. Problems with TMDB end up in the stored state, not in the error,
// which is for database failures. A refresh of a matched movie that fails
// keeps what was fetched before.
func (s *Service) lookup(ctx context.Context, it store.Item, prev *store.Metadata) (store.Metadata, error) {
	md := store.Metadata{ItemID: it.ID, MatchKey: MatchKey(it), FetchedAt: s.now()}
	refresh := prev != nil && prev.Status == store.MetaMatched && prev.TMDbID > 0 &&
		(prev.Source == "manual" || prev.MatchKey == md.MatchKey)
	var id int
	var err error
	switch {
	case prev != nil && prev.Source == "manual" && prev.TMDbID > 0:
		md.Source, id = "manual", prev.TMDbID
	case refresh:
		// The file name is unchanged: refresh the match instead of making
		// it again, which could pick another movie.
		md.Source, id = prev.Source, prev.TMDbID
	default:
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
	if refresh && md.Status != store.MetaMatched {
		kept := *prev
		kept.FetchedAt = md.FetchedAt
		md.Status = store.MetaError
		if md.Error == "" {
			md.Error = "TMDB lists another movie under its ID now"
		}
		md.Error = "refresh: " + md.Error
		return md, s.Store.SaveMetadata(ctx, kept)
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

// minAge is how old a file must be before it is removed as unused: an
// update running elsewhere, such as "homeflix match update" next to the
// server, stores the file before the database row that refers to it.
const minAge = time.Hour

// RemoveUnusedArtwork deletes files in dir that no metadata refers to:
// artwork of movies deleted for good, portraits of people and artwork of
// film series no movie lists any more, artwork replaced by a newer one at TMDB, and downloads left
// over from interrupted updates. Files younger than minAge are kept. It
// does not need TMDB access, so it also runs while TMDB is switched off.
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
	cols, err := st.AllCollections(ctx)
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
	for _, c := range cols {
		used[c.Poster], used[c.Backdrop] = true, true
	}
	removed := 0
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || used[name] {
			continue
		}
		if fi, err := de.Info(); err != nil || time.Since(fi.ModTime()) < minAge {
			continue
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
