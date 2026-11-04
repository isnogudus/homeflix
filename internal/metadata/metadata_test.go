package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

type fakeMovie struct {
	id                     int
	title, original, date  string
	overviewDE, overviewEN string
	imdb                   string
	cast                   []int // TMDB person IDs
}

type fakePerson struct {
	name, bioDE, bioEN, photo string
}

// fakeTMDB answers find, search, movie and image requests for a few movies.
type fakeTMDB struct {
	mu       sync.Mutex
	movies   []fakeMovie
	people   map[int]fakePerson
	requests []string
	fail     bool
}

func (f *fakeTMDB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.Path+"?"+r.URL.RawQuery)
	if f.fail {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	q := r.URL.Query()
	switch {
	case strings.HasPrefix(r.URL.Path, "/3/find/"):
		imdb := strings.TrimPrefix(r.URL.Path, "/3/find/")
		var res []map[string]int
		for _, m := range f.movies {
			if m.imdb == imdb {
				res = append(res, map[string]int{"id": m.id})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"movie_results": res})
	case r.URL.Path == "/3/search/movie":
		var res []map[string]any
		for _, m := range f.movies {
			if !strings.Contains(strings.ToLower(m.title+" "+m.original), strings.ToLower(q.Get("query"))) {
				continue
			}
			if y := q.Get("year"); y != "" && !strings.HasPrefix(m.date, y) {
				continue
			}
			res = append(res, map[string]any{"id": m.id, "title": m.title, "original_title": m.original, "release_date": m.date})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": res})
	case strings.HasPrefix(r.URL.Path, "/3/movie/"):
		for _, m := range f.movies {
			if r.URL.Path != fmt.Sprintf("/3/movie/%d", m.id) {
				continue
			}
			overview := m.overviewDE
			if q.Get("language") == "en-US" {
				overview = m.overviewEN
			}
			var cast []map[string]any
			for _, id := range m.cast {
				cast = append(cast, map[string]any{"id": id, "name": fmt.Sprint("Person ", id), "character": "Role"})
			}
			json.NewEncoder(w).Encode(map[string]any{
				"id": m.id, "title": m.title, "original_title": m.original, "overview": overview,
				"release_date": m.date, "imdb_id": m.imdb, "vote_average": 7.0, "vote_count": 10,
				"poster_path": fmt.Sprintf("/p%d.jpg", m.id), "backdrop_path": fmt.Sprintf("/b%d.jpg", m.id),
				"genres":  []map[string]any{{"id": 18, "name": "Drama"}},
				"credits": map[string]any{"cast": cast},
			})
			return
		}
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, "/3/person/"):
		var id int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/3/person/"), "%d", &id)
		p, ok := f.people[id]
		if !ok || q.Get("append_to_response") != "translations" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": id, "name": p.name, "biography": p.bioDE, "birthday": "1970-01-02", "profile_path": p.photo,
			"translations": map[string]any{"translations": []map[string]any{
				{"iso_639_1": "en", "data": map[string]any{"biography": p.bioEN}},
			}},
		})
	case strings.HasPrefix(r.URL.Path, "/t/p/"):
		w.Write([]byte("image " + r.URL.Path))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTMDB) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests
	f.requests = nil
	return r
}

type fixture struct {
	t     *testing.T
	store *store.Store
	fake  *fakeTMDB
	svc   *Service
	lib   store.Library
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fake := &fakeTMDB{movies: []fakeMovie{
		{id: 329865, title: "Arrival", original: "Arrival", date: "2016-09-02", overviewDE: "Außerirdische landen.", imdb: "tt2543164",
			cast: []int{9273, 555, 777}},
		{id: 70160, title: "Die Tribute von Panem - The Hunger Games", original: "The Hunger Games", date: "2012-03-12", overviewEN: "Every year ..."},
		{id: 687163, title: "Der Astronaut – Project Hail Mary", original: "Project Hail Mary", date: "2026-03-20", overviewDE: "Ein Lehrer im All."},
		{id: 11, title: "Heat", original: "Heat", date: "1996-02-15", overviewDE: "Cops and robbers."},
		{id: 99, title: "Delete Deleite", original: "Delete Deleite", date: "2013-05-01"},
		{id: 12, title: "Rush", original: "Rush", date: "2013-09-02", imdb: "tt1979320"},
	}}
	fake.people = map[int]fakePerson{
		9273: {name: "Amy Adams", bioEN: "An American actress.", photo: "/amy.jpg"},
		555:  {name: "Ohne Foto", bioDE: "Ein Nebendarsteller."},
		// 777 is unknown to TMDB.
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	f := &fixture{t: t, store: db, fake: fake, now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f.lib, _ = db.EnsureLibrary(context.Background(), "Filme", "movies")
	f.svc = &Service{
		Store:    db,
		TMDB:     &tmdb.Client{HTTP: srv.Client(), Token: "tok", APIBase: srv.URL + "/3", ImageBase: srv.URL + "/t/p/"},
		Language: "de-DE",
		Dir:      filepath.Join(dir, "metadata"),
		Workers:  2,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return f.now },
	}
	return f
}

func (f *fixture) add(id, title string, year int, imdb string) {
	f.t.Helper()
	err := f.store.ApplyScan(context.Background(), store.ScanChanges{NewItems: []store.Item{{
		ID: id, LibraryID: f.lib.ID, Type: "Movie", Path: "/m/" + id, Title: title, SortTitle: strings.ToLower(title),
		Year: year, IMDbID: imdb, DateCreated: f.now,
	}}})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) update() Report {
	f.t.Helper()
	rep, err := f.svc.Update(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) meta(id string) (store.Metadata, tmdb.Movie) {
	f.t.Helper()
	md, err := f.store.GetMetadata(context.Background(), id)
	if err != nil {
		f.t.Fatalf("metadata of %s: %v", id, err)
	}
	var m tmdb.Movie
	if md.Data != "" {
		json.Unmarshal([]byte(md.Data), &m)
	}
	return md, m
}

func TestUpdateMatchesAndFetches(t *testing.T) {
	f := newFixture(t)
	f.add("arrival", "Arrival", 2016, "tt2543164")
	f.add("hunger", "Die Tribute von Panem - The Hunger Games", 2012, "")
	f.add("mary", "Der Astronaut - Project Hail Mary", 2026, "")
	f.add("heat", "Heat", 1995, "") // released 1996 in Germany
	f.add("nothing", "Kein solcher Film", 2001, "")
	// A TV film TMDB lists no movie for: the search finds another movie
	// with a similar title, which the IMDb ID rules out.
	f.add("delete", "Delete", 2013, "tt2316306")
	// Same title and year, but TMDB knows another IMDb ID: another movie.
	f.add("rush", "Rush", 2013, "tt0000042")

	rep := f.update()
	if rep.Matched != 4 || rep.NotFound != 3 || rep.Failed != 0 {
		t.Errorf("report %+v", rep)
	}
	for id, want := range map[string]struct {
		tmdb   int
		source string
	}{"arrival": {329865, "imdb"}, "hunger": {70160, "search"}, "mary": {687163, "search"}, "heat": {11, "search"}} {
		md, _ := f.meta(id)
		if md.TMDbID != want.tmdb || md.Source != want.source || md.Status != store.MetaMatched {
			t.Errorf("%s: %+v", id, md)
		}
	}
	md, m := f.meta("arrival")
	if m.Overview != "Außerirdische landen." || md.Poster != "329865-poster-p329865.jpg" || md.Backdrop != "329865-backdrop-b329865.jpg" {
		t.Errorf("arrival: %+v %+v", md, m)
	}
	if b, err := os.ReadFile(filepath.Join(f.svc.Dir, md.Poster)); err != nil || string(b) != "image /t/p/w780/p329865.jpg" {
		t.Errorf("poster file: %q, %v", b, err)
	}
	if _, m := f.meta("hunger"); m.Overview != "Every year ..." {
		t.Errorf("English overview not used as fallback: %q", m.Overview)
	}
	if md, _ := f.meta("nothing"); md.Status != store.MetaNotFound {
		t.Errorf("unknown movie: %+v", md)
	}
	if md, _ := f.meta("delete"); md.Status != store.MetaNotFound || md.TMDbID != 0 {
		t.Errorf("partial title accepted for a file with an IMDb ID: %+v", md)
	}
	if md, _ := f.meta("rush"); md.Status != store.MetaNotFound || md.TMDbID != 0 || md.Poster != "" {
		t.Errorf("search result with another IMDb ID accepted: %+v", md)
	}

	// Nothing changed: no requests at all.
	f.fake.take()
	if rep := f.update(); rep.Unchanged != 7 {
		t.Errorf("second update: %+v", rep)
	}
	if r := f.fake.take(); len(r) != 0 {
		t.Errorf("second update sent %v", r)
	}
}

func TestRetriesAndRenames(t *testing.T) {
	f := newFixture(t)
	f.add("arrival", "Arrival", 2016, "")
	f.fake.fail = true
	if rep := f.update(); rep.Failed != 1 {
		t.Errorf("failing TMDB: %+v", rep)
	}
	f.fake.fail = false
	if rep := f.update(); rep.Unchanged != 1 {
		t.Errorf("error retried too early: %+v", rep)
	}
	f.now = f.now.Add(retryErrors)
	if rep := f.update(); rep.Matched != 1 {
		t.Errorf("error not retried after a day: %+v", rep)
	}

	// A manual match survives a rename; an automatic one is redone.
	ctx := context.Background()
	items, _ := f.store.Movies(ctx)
	if _, err := f.svc.Match(ctx, items[0], 11); err != nil {
		t.Fatal(err)
	}
	renamed := items[0]
	renamed.Year = 2017
	f.store.ApplyScan(ctx, store.ScanChanges{UpdatedItems: []store.Item{renamed}})
	f.update()
	if md, _ := f.meta("arrival"); md.TMDbID != 11 || md.Source != "manual" {
		t.Errorf("manual match lost after rename: %+v", md)
	}
	f.store.DeleteMetadata(ctx, "arrival")
	f.update()
	if md, _ := f.meta("arrival"); md.Source != "search" {
		t.Errorf("after clearing the manual match: %+v", md)
	}
}

func TestPickExactOnly(t *testing.T) {
	res := []tmdb.SearchResult{{ID: 1, Title: "Delete Deleite", ReleaseDate: "2013-06-01"}}
	if _, ok := pick(res, "Delete", "Delete", 2013, true); ok {
		t.Error("partial title accepted with exactOnly")
	}
	if _, ok := pick(res, "Delete", "Delete", 2013, false); !ok {
		t.Error("partial title with exact year rejected without exactOnly")
	}
}

func TestPick(t *testing.T) {
	r := func(id int, title, date string) tmdb.SearchResult {
		return tmdb.SearchResult{ID: id, Title: title, ReleaseDate: date}
	}
	for name, tc := range map[string]struct {
		res   []tmdb.SearchResult
		title string
		year  int
		want  int
	}{
		"exact":               {[]tmdb.SearchResult{r(1, "Alien", "1979-05-25"), r(2, "Alien", "2024-01-01")}, "Alien", 1979, 1},
		"one year off":        {[]tmdb.SearchResult{r(1, "Heat", "1996-02-15")}, "Heat", 1995, 1},
		"two years off":       {[]tmdb.SearchResult{r(1, "Heat", "1997-01-01")}, "Heat", 1995, 0},
		"other title":         {[]tmdb.SearchResult{r(1, "Heathers", "1995-01-01")}, "Heat", 1995, 0},
		"punctuation":         {[]tmdb.SearchResult{r(1, "Wall·E", "2008-06-22")}, "Wall-E", 2008, 1},
		"contained, exact":    {[]tmdb.SearchResult{r(1, "Tron: Ares", "2025-10-08")}, "Tron Ares 3D", 2025, 1},
		"contained, year off": {[]tmdb.SearchResult{r(1, "Tron: Ares", "2025-10-08")}, "Tron Ares 3D", 2024, 0},
		"subtitle":            {[]tmdb.SearchResult{r(1, "Dune: Part Two", "2024-02-27")}, "Dune - Part Two", 2024, 1},
		"no year, exact":      {[]tmdb.SearchResult{r(1, "Little Women", "2019-12-25")}, "Little Women", 0, 1},
		"no year, partial":    {[]tmdb.SearchResult{r(1, "Little Women", "2019-12-25")}, "Little Woman", 0, 0},
		"empty":               {nil, "Heat", 1995, 0},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := pick(tc.res, tc.title, tc.title, tc.year, false)
			if (tc.want == 0) == ok || (ok && got != tc.want) {
				t.Errorf("pick = %d, %v; want %d", got, ok, tc.want)
			}
		})
	}
}

func TestRemoveUnusedArtwork(t *testing.T) {
	f := newFixture(t)
	f.add("arrival", "Arrival", 2016, "tt2543164")
	f.update()
	md, _ := f.meta("arrival")
	dir := f.svc.Dir
	// Unused files are removed once they are older than an hour; younger
	// ones may belong to an update in another process.
	old := time.Now().Add(-2 * time.Hour)
	for name, age := range map[string]bool{"11-poster-old.jpg": true, "12-profile-new.jpg": false,
		".download-fresh": false, ".download-stale": true} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("x"), 0o644)
		if age {
			os.Chtimes(p, old, old)
		}
	}
	for _, name := range []string{md.Poster, md.Backdrop} {
		os.Chtimes(filepath.Join(dir, name), old, old)
	}

	n, err := RemoveUnusedArtwork(context.Background(), f.store, dir, nil)
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v; want 2", n, err)
	}
	for name, want := range map[string]bool{
		md.Poster: true, md.Backdrop: true, "11-poster-old.jpg": false, "12-profile-new.jpg": true,
		".download-fresh": true, ".download-stale": false,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s: exists %v, want %v", name, err == nil, want)
		}
	}
	// A movie deleted for good takes its artwork with it.
	f.store.ApplyScan(context.Background(), store.ScanChanges{DeletedItemIDs: []string{"arrival"}})
	if n, _ := RemoveUnusedArtwork(context.Background(), f.store, dir, nil); n != 2 {
		t.Errorf("after deleting the movie removed %d, want 2", n)
	}
	if n, err := RemoveUnusedArtwork(context.Background(), f.store, filepath.Join(dir, "absent"), nil); n != 0 || err != nil {
		t.Errorf("missing directory: %d, %v", n, err)
	}
}

func TestPeople(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.add("arrival", "Arrival", 2016, "tt2543164")
	rep := f.update()
	if rep.Matched != 1 || rep.People != 2 || rep.PeopleNotFound != 1 || rep.PeopleFailed != 0 || !rep.Changed() {
		t.Fatalf("report %+v", rep)
	}
	people, _ := f.store.AllPeople(ctx)
	amy := people[9273]
	var d tmdb.PersonDetails
	json.Unmarshal([]byte(amy.Data), &d)
	if amy.Status != store.MetaMatched || amy.Photo != "9273-profile-amy.jpg" || d.Biography != "An American actress." ||
		d.Birthday != "1970-01-02" {
		t.Errorf("Amy Adams: %+v %+v", amy, d)
	}
	if b, err := os.ReadFile(filepath.Join(f.svc.Dir, amy.Photo)); err != nil || string(b) != "image /t/p/h632/amy.jpg" {
		t.Errorf("photo: %q, %v", b, err)
	}
	if p := people[555]; p.Status != store.MetaMatched || p.Photo != "" || !strings.Contains(p.Data, "Ein Nebendarsteller.") {
		t.Errorf("person without photo: %+v", p)
	}
	if p := people[777]; p.Status != store.MetaNotFound {
		t.Errorf("unknown person: %+v", p)
	}

	// Looked up once: the next update sends nothing.
	f.fake.take()
	if rep := f.update(); rep.Changed() {
		t.Errorf("second update: %+v", rep)
	}
	if r := f.fake.take(); len(r) != 0 {
		t.Errorf("second update sent %v", r)
	}
	// A photo removed from the cache is fetched again.
	os.Remove(filepath.Join(f.svc.Dir, amy.Photo))
	if rep := f.update(); rep.People != 1 {
		t.Errorf("after removing the photo: %+v", rep)
	}
	if n, _ := RemoveUnusedArtwork(ctx, f.store, f.svc.Dir, nil); n != 0 {
		t.Errorf("removed %d files still in use", n)
	}

	// Without the movie nobody lists them: they and the photo go.
	f.store.ApplyScan(ctx, store.ScanChanges{DeletedItemIDs: []string{"arrival"}})
	f.update()
	if people, _ := f.store.AllPeople(ctx); len(people) != 0 {
		t.Errorf("people of a deleted movie kept: %+v", people)
	}
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(filepath.Join(f.svc.Dir, amy.Photo), old, old)
	RemoveUnusedArtwork(ctx, f.store, f.svc.Dir, nil)
	if _, err := os.Stat(filepath.Join(f.svc.Dir, amy.Photo)); !os.IsNotExist(err) {
		t.Errorf("photo of a forgotten person kept: %v", err)
	}
}
