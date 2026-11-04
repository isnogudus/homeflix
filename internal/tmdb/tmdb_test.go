package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The responses are trimmed versions of what the TMDB API v3 returns.
const arrivalDE = `{
  "id": 329865, "imdb_id": "tt2543164", "title": "Arrival", "original_title": "Arrival",
  "overview": "Als zwölf außerirdische Raumschiffe landen ...", "tagline": "Warum sind sie hier?",
  "release_date": "2016-09-02", "vote_average": 7.6, "vote_count": 19000,
  "poster_path": "/de-poster.jpg", "backdrop_path": "/backdrop.jpg",
  "genres": [{"id": 18, "name": "Drama"}, {"id": 878, "name": "Science Fiction"}],
  "production_companies": [{"id": 1, "name": "FilmNation Entertainment"}],
  "credits": {
    "cast": [{"id": 9273, "name": "Amy Adams", "character": "Louise Banks", "order": 0},
             {"id": 10859, "name": "Jeremy Renner", "character": "Ian Donnelly", "order": 1}],
    "crew": [{"id": 137427, "name": "Denis Villeneuve", "job": "Director"},
             {"id": 1, "name": "Eric Heisserer", "job": "Screenplay"},
             {"id": 2, "name": "Someone", "job": "Gaffer"}]
  },
  "release_dates": {"results": [
    {"iso_3166_1": "US", "release_dates": [{"certification": "NR"}, {"certification": "PG-13"}]},
    {"iso_3166_1": "DE", "release_dates": [{"certification": ""}, {"certification": "12"}]}
  ]}
}`

const amyAdams = `{
  "id": 9273, "name": "Amy Adams", "biography": "", "birthday": "1974-08-20", "deathday": null,
  "place_of_birth": "Vicenza, Veneto, Italy", "imdb_id": "nm0010736", "profile_path": "/amy.jpg",
  "known_for_department": "Acting",
  "translations": {"translations": [
    {"iso_639_1": "fr", "data": {"biography": "Amy Lou Adams est une actrice ..."}},
    {"iso_639_1": "en", "data": {"biography": " Amy Lou Adams is an American actress ... "}}
  ]}
}`

type fakeTMDB struct {
	*httptest.Server
	requests  atomic.Int32
	auth      atomic.Value
	failFirst atomic.Bool
}

func newFake(t *testing.T) (*fakeTMDB, *Client) {
	t.Helper()
	f := &fakeTMDB{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.auth.Store(r.Header.Get("Authorization"))
		if f.failFirst.CompareAndSwap(true, false) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/3/find/tt2543164" && q.Get("external_source") == "imdb_id":
			w.Write([]byte(`{"movie_results":[{"id":329865}],"tv_results":[]}`))
		case strings.HasPrefix(r.URL.Path, "/3/find/"):
			w.Write([]byte(`{"movie_results":[]}`))
		case r.URL.Path == "/3/search/movie":
			if q.Get("query") == "Arrival" && q.Get("year") == "2016" && q.Get("language") == "de-DE" {
				w.Write([]byte(`{"results":[{"id":329865,"title":"Arrival","original_title":"Arrival","release_date":"2016-09-02"}]}`))
				return
			}
			w.Write([]byte(`{"results":[]}`))
		case r.URL.Path == "/3/movie/329865" && q.Get("append_to_response") == "credits,release_dates":
			w.Write([]byte(arrivalDE))
		case r.URL.Path == "/3/person/9273" && q.Get("append_to_response") == "translations":
			// No German biography: the English one comes in the translations.
			w.Write([]byte(amyAdams))
		case r.URL.Path == "/t/p/w780/de-poster.jpg":
			w.Write([]byte("JPEG"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	c := &Client{HTTP: f.Client(), Token: "tok", APIBase: f.URL + "/3", ImageBase: f.URL + "/t/p/"}
	return f, c
}

func TestFindSearchMovieImage(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	id, err := c.FindByIMDb(ctx, "tt2543164")
	if err != nil || id != 329865 {
		t.Fatalf("find: %d, %v", id, err)
	}
	if got := f.auth.Load(); got != "Bearer tok" {
		t.Errorf("Authorization %q", got)
	}
	if _, err := c.FindByIMDb(ctx, "tt0000001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown IMDb ID: %v", err)
	}
	res, err := c.Search(ctx, "Arrival", 2016, "de-DE")
	if err != nil || len(res) != 1 || res[0].Year() != 2016 {
		t.Fatalf("search: %+v, %v", res, err)
	}
	m, err := c.Movie(ctx, 329865, "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Arrival" || m.IMDbID != "tt2543164" || m.Tagline != "Warum sind sie hier?" ||
		strings.Join(m.Genres, ",") != "Drama,Science Fiction" || m.Rating != 7.6 || m.Certification != "FSK-12" {
		t.Errorf("movie: %+v", m)
	}
	var kinds []string
	for _, p := range m.People {
		kinds = append(kinds, p.Type+":"+p.Name)
	}
	if strings.Join(kinds, ",") != "Director:Denis Villeneuve,Writer:Eric Heisserer,Actor:Amy Adams,Actor:Jeremy Renner" {
		t.Errorf("people: %v", kinds)
	}
	if m.People[2].Role != "Louise Banks" {
		t.Errorf("role: %+v", m.People[2])
	}
	us, _ := c.Movie(ctx, 329865, "en-US")
	if us.Certification != "PG-13" {
		t.Errorf("US certification %q", us.Certification)
	}
	img, err := c.Image(ctx, "/de-poster.jpg", "w780")
	if err != nil || string(img) != "JPEG" {
		t.Errorf("image: %q, %v", img, err)
	}
	if _, err := c.Movie(ctx, 1, "de-DE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown movie: %v", err)
	}
}

func TestRetryAfterTooManyRequests(t *testing.T) {
	f, c := newFake(t)
	f.failFirst.Store(true)
	if id, err := c.FindByIMDb(context.Background(), "tt2543164"); err != nil || id != 329865 {
		t.Fatalf("after 429: %d, %v", id, err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Token: "bad", APIBase: srv.URL}
	if _, err := c.FindByIMDb(context.Background(), "tt1"); err == nil || !strings.Contains(err.Error(), "Token") {
		t.Errorf("err %v", err)
	}
}

func TestPerson(t *testing.T) {
	_, c := newFake(t)
	p, err := c.Person(context.Background(), 9273, "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	want := PersonDetails{ID: 9273, Name: "Amy Adams", Biography: "Amy Lou Adams is an American actress ...",
		Birthday: "1974-08-20", PlaceOfBirth: "Vicenza, Veneto, Italy", IMDbID: "nm0010736", ProfilePath: "/amy.jpg"}
	if p != want {
		t.Errorf("person:\n got %+v\nwant %+v", p, want)
	}
	if _, err := c.Person(context.Background(), 1, "de-DE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown person: %v", err)
	}
}
