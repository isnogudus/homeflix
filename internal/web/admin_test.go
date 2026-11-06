package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/hls"
	"homeflix/internal/library"
	"homeflix/internal/metadata"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

// adminFixture signs in the admin from the home network.
func adminFixture(t *testing.T, setup ...func(*Server)) (*fixture, *http.Cookie) {
	t.Helper()
	f := newFixture(t, setup...)
	_, c := f.login(lan, "admin", "pw-admin")
	if c == nil {
		t.Fatal("admin login failed")
	}
	return f, c
}

func csrfOf(c *http.Cookie) string { return (&session{raw: c.Value}).CSRF() }

// post sends a form with the session's CSRF token.
func (f *fixture) post(path string, c *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", csrfOf(c))
	return f.do(lan, "POST", path, form, c)
}

func (f *fixture) user(name string) store.User {
	f.t.Helper()
	u, err := f.db.UserByName(context.Background(), name)
	if err != nil {
		f.t.Fatalf("user %s: %v", name, err)
	}
	return u
}

func TestAdminPages(t *testing.T) {
	scans := 0
	f, c := adminFixture(t, func(s *Server) {
		s.Version = "v-test"
		s.Started = time.Now().Add(-3 * time.Hour)
		s.Catalog = &catalog.Catalog{Store: s.Store}
		s.RequestScan = func() { scans++ }
		s.ScanStatus = func() ScanStatus {
			return ScanStatus{Reason: "requested", Started: time.Now().Add(-time.Minute), Finished: time.Now(),
				Report: library.Report{Libraries: []library.LibraryReport{{Name: "Filme", Movies: 1732, Added: 2}}, Took: 44 * time.Millisecond}}
		}
		s.Settings = []Setting{{Name: "listen on", Value: ":8096"}}
		s.HLS = &hls.Manager{}
	})
	for path, want := range map[string]string{
		"/admin/":         "v-test",
		"/admin/users":    "Neuer Benutzer",
		"/admin/library":  "Filme: 1732 Filme, 2 neu",
		"/admin/metadata": "Alle Filme haben Metadaten.",
		"/admin/settings": "listen on",
	} {
		rec := f.do(lan, "GET", path, nil, c)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d, lacks %q", path, rec.Code, want)
		}
	}
	if rec := f.do(lan, "GET", "/admin/", nil, c); !strings.Contains(rec.Body.String(), "3 h 0 min") ||
		!strings.Contains(rec.Body.String(), "TMDB ist aus") ||
		!strings.Contains(rec.Body.String(), "<dt>Keyframe-Index</dt><dd>0 / 0</dd>") {
		t.Errorf("overview: %s", rec.Body.String())
	}
	rec := f.post("/admin/library/scan", c, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/library?ok=scanRequested" || scans != 1 {
		t.Errorf("scan: %d %v, %d scans", rec.Code, rec.Header(), scans)
	}
	if rec := f.do(lan, "GET", "/admin/library?ok=scanRequested", nil, c); !strings.Contains(rec.Body.String(), "Ein Scan wurde gestartet.") {
		t.Error("message after the scan missing")
	}
	if rec := f.do(lan, "GET", "/admin/library?ok=<script>", nil, c); strings.Contains(rec.Body.String(), "&lt;script") || strings.Contains(rec.Body.String(), "<script>") {
		t.Error("unknown message key shown")
	}
	// Forms need the token.
	if rec := f.do(lan, "POST", "/admin/library/scan", url.Values{}, c); rec.Code != http.StatusForbidden || scans != 1 {
		t.Errorf("scan without form token: %d", rec.Code)
	}
}

func TestAdminUsers(t *testing.T) {
	f, c := adminFixture(t)
	ctx := context.Background()

	if rec := f.post("/admin/users", c, url.Values{"name": {"bert"}, "password": {"x"}, "confirm": {"y"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=passwordMismatch") {
		t.Errorf("mismatch: %v", rec.Header())
	}
	rec := f.post("/admin/users", c, url.Values{"name": {"bert"}, "password": {"geheim"}, "confirm": {"geheim"}, "admin": {"on"}})
	bert := f.user("bert")
	if rec.Header().Get("Location") != "/admin/users/"+bert.ID+"?ok=userCreated" || !bert.IsAdmin {
		t.Errorf("create: %v %+v", rec.Header(), bert)
	}
	if _, bc := f.login(lan, "bert", "geheim"); bc == nil {
		t.Error("new user cannot sign in")
	}
	if rec := f.post("/admin/users", c, url.Values{"name": {"BERT"}, "password": {"a"}, "confirm": {"a"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=userExists") {
		t.Errorf("duplicate: %v", rec.Header())
	}

	// Anna signs in; a new password signs her out.
	anna := f.user("anna")
	_, ac := f.login(lan, "anna", "pw-anna")
	if rec := f.do(lan, "GET", "/admin/users/"+anna.ID, nil, c); rec.Code != 200 || !strings.Contains(rec.Body.String(), "homeflix web") {
		t.Errorf("user page: %d", rec.Code)
	}
	f.post("/admin/users/"+anna.ID+"/password", c, url.Values{"password": {"neu"}, "confirm": {"neu"}})
	if rec := f.do(lan, "GET", "/web/", nil, ac); rec.Code != http.StatusFound {
		t.Error("anna still signed in after the new password")
	}
	_, ac = f.login(lan, "anna", "neu")
	if ac == nil {
		t.Fatal("anna cannot sign in with the new password")
	}
	// End her session from the admin UI.
	toks, _ := f.db.Tokens(ctx, anna.ID)
	f.post("/admin/users/"+anna.ID+"/sessions/"+toks[0].SessionID+"/end", c, nil)
	if rec := f.do(lan, "GET", "/web/", nil, ac); rec.Code != http.StatusFound {
		t.Error("anna's session survived")
	}

	// Conversion levels.
	if body := f.do(lan, "GET", "/admin/users/"+anna.ID, nil, c).Body.String(); !strings.Contains(body, `value="all" checked`) {
		t.Errorf("conversion choice: %s", body)
	}
	if rec := f.post("/admin/users/"+anna.ID+"/conversion", c, url.Values{"conversion": {"none"}}); !strings.HasSuffix(rec.Header().Get("Location"), "ok=conversionChanged") ||
		f.user("anna").Conversion != store.ConversionNone {
		t.Errorf("conversion none: %v %q", rec.Header(), f.user("anna").Conversion)
	}
	if rec := f.post("/admin/users/"+anna.ID+"/download", c, url.Values{"download": {"on"}}); !strings.HasSuffix(rec.Header().Get("Location"), "ok=downloadChanged") ||
		!f.user("anna").CanDownload {
		t.Errorf("download on: %v", rec.Header())
	}
	if f.post("/admin/users/"+anna.ID+"/download", c, url.Values{}); f.user("anna").CanDownload {
		t.Error("download right not removed")
	}
	if rec := f.post("/admin/users/"+anna.ID+"/conversion", c, url.Values{"conversion": {"video"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=conversionInvalid") ||
		f.user("anna").Conversion != store.ConversionNone {
		t.Errorf("invalid conversion: %v", rec.Header())
	}

	f.post("/admin/users/"+anna.ID+"/admin", c, url.Values{"admin": {"on"}})
	if !f.user("anna").IsAdmin {
		t.Error("admin right not granted")
	}
	admin := f.user("admin")
	if rec := f.post("/admin/users/"+admin.ID+"/admin", c, url.Values{}); !strings.HasSuffix(rec.Header().Get("Location"), "err=ownAdmin") || !f.user("admin").IsAdmin {
		t.Errorf("own rights removed: %v", rec.Header())
	}
	if rec := f.post("/admin/users/"+admin.ID+"/delete", c, url.Values{"confirm": {"admin"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=ownDelete") {
		t.Errorf("own delete: %v", rec.Header())
	}
	if rec := f.post("/admin/users/"+anna.ID+"/delete", c, url.Values{"confirm": {"bert"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=deleteConfirm") {
		t.Errorf("wrong confirmation: %v", rec.Header())
	}
	f.post("/admin/users/"+anna.ID+"/delete", c, url.Values{"confirm": {"Anna"}})
	if _, err := f.db.UserByName(ctx, "anna"); err == nil {
		t.Error("anna not deleted")
	}
	if rec := f.do(lan, "GET", "/admin/users/nobody", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user: %d", rec.Code)
	}
}

func TestOwnPasswordSignsOut(t *testing.T) {
	f, c := adminFixture(t)
	admin := f.user("admin")
	rec := f.post("/admin/users/"+admin.ID+"/password", c, url.Values{"password": {"neu"}, "confirm": {"neu"}})
	if !strings.HasPrefix(rec.Header().Get("Location"), "/web/login") {
		t.Errorf("own password: %v", rec.Header())
	}
}

// fakeTMDB answers a search and the details of one movie.
func fakeTMDB(t *testing.T) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/3/search/movie":
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"id": 1, "title": "Die Säulen der Erde", "original_title": "The Pillars of the Earth", "release_date": "2010-07-23", "popularity": 5},
				{"id": 2, "title": "Säulen", "release_date": "1999-01-01", "popularity": 1},
			}})
		case r.URL.Path == "/3/movie/1":
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "title": "Die Säulen der Erde", "overview": "Ein Dom.",
				"release_date": "2010-07-23"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &tmdb.Client{HTTP: srv.Client(), Token: "tok", APIBase: srv.URL + "/3", ImageBase: srv.URL + "/t/p/"}
}

func TestAdminMetadata(t *testing.T) {
	reloads, scans := 0, 0
	var svc *metadata.Service
	f, c := adminFixture(t, func(s *Server) {
		svc = &metadata.Service{Store: s.Store, TMDB: fakeTMDB(t), Language: "de-DE", Dir: filepath.Join(t.TempDir(), "metadata")}
		s.Metadata = svc
		s.Reload = func() { reloads++ }
		s.RequestScan = func() { scans++ }
	})
	ctx := context.Background()
	lib, _ := f.db.EnsureLibrary(ctx, "Filme", "movies")
	err := f.db.ApplyScan(ctx, store.ScanChanges{NewItems: []store.Item{{ID: "m1", LibraryID: lib.ID, Type: "Movie",
		Path: "/m/Die Säulen der Erde (2010).mkv", Title: "Die Säulen der Erde", SortTitle: "saulen der erde", Year: 2010,
		DateCreated: time.Now()}}})
	if err != nil {
		t.Fatal(err)
	}
	f.db.SaveMetadata(ctx, store.Metadata{ItemID: "m1", Source: "search", MatchKey: "x", Status: store.MetaNotFound, FetchedAt: time.Now()})

	rec := f.do(lan, "GET", "/admin/metadata", nil, c)
	if !strings.Contains(rec.Body.String(), `href="/admin/metadata/m1"`) || !strings.Contains(rec.Body.String(), "nicht gefunden") {
		t.Errorf("unmatched list: %s", rec.Body.String())
	}
	rec = f.do(lan, "GET", "/admin/metadata/m1?q=S%C3%A4ulen+der+Erde&year=", nil, c)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "The Pillars of the Earth") || !strings.Contains(body, `name="tmdb_id" value="1"`) ||
		strings.Index(body, "Die Säulen der Erde</td>") > strings.Index(body, "Säulen</td>") {
		t.Errorf("search results: %d %s", rec.Code, body)
	}

	rec = f.post("/admin/metadata/m1/match", c, url.Values{"tmdb_id": {"1"}})
	if rec.Header().Get("Location") != "/admin/metadata/m1?ok=matchSaved" || reloads != 1 {
		t.Fatalf("match: %v, %d reloads", rec.Header(), reloads)
	}
	md, _ := f.db.GetMetadata(ctx, "m1")
	if md.Status != store.MetaMatched || md.Source != "manual" || md.TMDbID != 1 {
		t.Errorf("after match: %+v", md)
	}
	if rec := f.do(lan, "GET", "/admin/metadata/m1", nil, c); !strings.Contains(rec.Body.String(), "themoviedb.org/movie/1") {
		t.Error("current match not shown")
	}
	if rec := f.post("/admin/metadata/m1/match", c, url.Values{"tmdb_id": {"abc"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=tmdbID") {
		t.Errorf("bad ID: %v", rec.Header())
	}
	if rec := f.post("/admin/metadata/m1/match", c, url.Values{"tmdb_id": {"99"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=matchFailed") {
		t.Errorf("unknown ID: %v", rec.Header())
	}

	f.post("/admin/metadata/m1/clear", c, nil)
	if _, err := f.db.GetMetadata(ctx, "m1"); err == nil || scans != 1 || reloads != 2 {
		t.Errorf("clear: %v, %d scans, %d reloads", err, scans, reloads)
	}
	if rec := f.do(lan, "GET", "/admin/metadata?q=s%C3%A4ulen", nil, c); !strings.Contains(rec.Body.String(), "noch nicht abgefragt") {
		t.Error("search over all movies")
	}
	if rec := f.do(lan, "GET", "/admin/metadata/nope", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("unknown movie: %d", rec.Code)
	}
}
