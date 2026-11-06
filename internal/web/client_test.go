package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/hls"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

const (
	slaveID   = "a2000000000000000000000000000001" // MP4, h264 + AAC: plays in browsers
	arrivalID = "a2000000000000000000000000000002" // MKV, HEVC + AC3: does not yet
	splitID   = "a2000000000000000000000000000003" // MP4 in two parts
	remuxID   = "a2000000000000000000000000000004" // MKV, h264 + AC3: plays remuxed
	libID     = "11111111111111111111111111111111"
)

func probeFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "media", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// clientFixture serves the web client over a small catalog whose files
// exist; it returns the fixture, anna's cookie and the file contents.
func clientFixture(t *testing.T, meta ...map[string]store.Metadata) (*fixture, *http.Cookie, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	contents := map[string]string{}
	file := func(id, item, version string, part int, name, probe string) store.MediaFile {
		p := filepath.Join(dir, name)
		data := strings.Repeat(id+";", 50)
		os.WriteFile(p, []byte(data), 0o644)
		contents[id] = data
		return store.MediaFile{ID: id, ItemID: item, Version: version, Part: part, Path: p, Size: int64(len(data)), Probe: probe}
	}
	slave, arrival := probeFixture(t, "twelve-years"), probeFixture(t, "arrival")
	now := time.Now()
	items := []store.Item{
		{ID: slaveID, LibraryID: libID, Type: "Movie", Title: "12 Years a Slave", SortTitle: "12 years a slave", Year: 2013,
			DateCreated: now.Add(-time.Hour), Files: []store.MediaFile{file("f1", slaveID, "", 0, "slave.mp4", slave)}},
		{ID: arrivalID, LibraryID: libID, Type: "Movie", Title: "Arrival", SortTitle: "arrival", Year: 2016,
			DateCreated: now, Files: []store.MediaFile{file("f2", arrivalID, "", 0, "arrival.mkv", arrival)}},
		{ID: splitID, LibraryID: libID, Type: "Movie", Title: "Beta", SortTitle: "beta", Year: 2001,
			DateCreated: now.Add(-2 * time.Hour), Files: []store.MediaFile{
				file("f3", splitID, "", 1, "beta part1.mp4", slave), file("f4", splitID, "", 2, "beta part2.mp4", slave)}},
	}
	var f *fixture
	f = newFixture(t, func(s *Server) {
		s.Catalog = &catalog.Catalog{Store: s.Store}
		s.Catalog.Set(catalog.Build([]store.Library{{ID: libID, Name: "Filme", CollectionType: "movies"}}, items,
			catalog.Options{Locale: "de", Metadata: firstOr(meta)}))
	})
	_, c := f.login(lan, "anna", "pw-anna")
	return f, c, contents
}

func TestBrowse(t *testing.T) {
	f, c, _ := clientFixture(t)
	rec := f.do(lan, "GET", "/web/", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Zuletzt hinzugefügt") || !strings.Contains(rec.Body.String(), "/web/movies/"+arrivalID) {
		t.Errorf("home: %d %s", rec.Code, rec.Body.String())
	}
	titles := func(body string) string {
		var out []string
		for _, m := range regexp.MustCompile(`<span class="name">([^<]*)</span>`).FindAllStringSubmatch(body, -1) {
			out = append(out, html.UnescapeString(m[1]))
		}
		return strings.Join(out, ", ")
	}
	for q, want := range map[string]string{
		"":                "12 Years a Slave, Arrival, Beta",
		"?sort=sortAdded": "Arrival, 12 Years a Slave, Beta",
		"?sort=sortYear":  "Arrival, 12 Years a Slave, Beta",
		"?letter=B":       "Beta",
		"?letter=%23":     "12 Years a Slave",
	} {
		rec := f.do(lan, "GET", "/web/movies"+q, nil, c)
		if got := titles(rec.Body.String()); rec.Code != 200 || got != want {
			t.Errorf("movies%s: %d %q, want %q", q, rec.Code, got, want)
		}
	}
	rec = f.do(lan, "GET", "/web/search?q=arr", nil, c)
	if got := titles(rec.Body.String()); got != "Arrival" {
		t.Errorf("search: %q", got)
	}
	for _, p := range []string{"/web/collections", "/web/account"} {
		if rec := f.do(lan, "GET", p, nil, c); rec.Code != 200 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/web/movies/nope", "/web/collections/nope", "/web/people/nope", "/web/play/nope"} {
		if rec := f.do(lan, "GET", p, nil, c); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

// playerConfig reads the player configuration from a play page.
func playerConfig(t *testing.T, body string) map[string]any {
	t.Helper()
	m := regexp.MustCompile(`data-config="([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no player config in %s", body)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDetailsAndPlayer(t *testing.T) {
	f, c, contents := clientFixture(t)
	rec := f.do(lan, "GET", "/web/movies/"+slaveID, nil, c)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "/web/play/"+slaveID) || !strings.Contains(body, "Der Browser spielt") {
		t.Fatalf("details: %d %s", rec.Code, body)
	}
	rec = f.do(lan, "GET", "/web/movies/"+arrivalID, nil, c)
	if !strings.Contains(rec.Body.String(), "Browser können dieses Video noch nicht abspielen") || strings.Contains(rec.Body.String(), "/web/play/") {
		t.Errorf("unplayable details: %s", rec.Body.String())
	}
	if rec := f.do(lan, "GET", "/web/play/"+arrivalID, nil, c); strings.Contains(rec.Body.String(), "data-config") {
		t.Error("player for an unplayable movie")
	}

	rec = f.do(lan, "GET", "/web/play/"+splitID+"?part=1&t=90", nil, c)
	cfg := playerConfig(t, rec.Body.String())
	parts, _ := cfg["parts"].([]any)
	if len(parts) != 2 || cfg["part"] != 1.0 || cfg["start"] != 90.0 || cfg["csrf"] != csrfOf(c) {
		t.Fatalf("player config: %v", cfg)
	}
	second := parts[1].(map[string]any)
	audio, _ := second["audio"].([]any)
	if second["item"] != "f4" || second["direct"] != "/web/stream/f4" || second["hls"] != "" || len(audio) != 3 ||
		second["duration"].(float64) < 7000 {
		t.Errorf("second part: %v", second)
	}
	if !strings.Contains(rec.Body.String(), `<script src="/web/static/player.js?v=`+assetVersions["player.js"]+`" defer></script>`) {
		t.Error("player script missing")
	}

	// The stream needs the session and answers ranges.
	if rec := f.do(lan, "GET", "/web/stream/f1", nil, nil); rec.Code != http.StatusFound {
		t.Errorf("stream without session: %d", rec.Code)
	}
	req := f.do(lan, "GET", "/web/stream/f1", nil, c)
	if req.Code != 200 || req.Header().Get("Content-Type") != "video/mp4" || req.Body.String() != contents["f1"] {
		t.Errorf("stream: %d %v", req.Code, req.Header())
	}
	if rec := f.do(lan, "GET", "/web/subtitles/f1/2", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("subtitles without extraction: %d", rec.Code)
	}
}

func TestProgressAndResume(t *testing.T) {
	f, c, _ := clientFixture(t)
	ctx := context.Background()
	report := func(item, event string, secs string) {
		t.Helper()
		rec := f.post("/web/progress", c, url.Values{"item": {item}, "source": {slaveID}, "event": {event}, "position": {secs}})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s %s: %d", item, event, rec.Code)
		}
	}
	report(slaveID, "start", "0")
	report(slaveID, "progress", "1800")
	report(slaveID, "stop", "1805")
	anna := f.user("anna")
	d, _ := f.db.GetUserData(ctx, anna.ID, slaveID)
	if d.Position != 1805*time.Second || d.Played {
		t.Errorf("after stop: %+v", d)
	}
	rec := f.do(lan, "GET", "/web/movies/"+slaveID, nil, c)
	if !strings.Contains(rec.Body.String(), "Fortsetzen bei 30:05") || !strings.Contains(rec.Body.String(), "t=1805") {
		t.Errorf("resume button: %s", rec.Body.String())
	}
	if rec := f.do(lan, "GET", "/web/", nil, c); !strings.Contains(rec.Body.String(), "Weiterschauen") {
		t.Error("home lacks continue watching")
	}
	// Past 90 % the movie counts as watched.
	report(slaveID, "stop", "7500")
	if d, _ := f.db.GetUserData(ctx, anna.ID, slaveID); !d.Played || d.Position != 0 {
		t.Errorf("after the end: %+v", d)
	}
	// Finishing the last part marks the split movie watched.
	report("f4", "stop", "7600")
	if d, _ := f.db.GetUserData(ctx, anna.ID, splitID); !d.Played {
		t.Errorf("split movie after its last part: %+v", d)
	}
	// Progress needs the form token.
	if rec := f.do(lan, "POST", "/web/progress", url.Values{"item": {slaveID}, "event": {"stop"}, "position": {"10"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("progress without token: %d", rec.Code)
	}
}

func TestPlayedAndFavorite(t *testing.T) {
	f, c, _ := clientFixture(t)
	ctx := context.Background()
	anna := f.user("anna")
	f.post("/web/movies/"+splitID+"/played", c, url.Values{"played": {"on"}})
	for _, id := range []string{splitID, "f4"} {
		if d, _ := f.db.GetUserData(ctx, anna.ID, id); !d.Played {
			t.Errorf("%s not watched", id)
		}
	}
	f.post("/web/movies/"+splitID+"/favorite", c, url.Values{"favorite": {"on"}})
	if d, _ := f.db.GetUserData(ctx, anna.ID, splitID); !d.IsFavorite {
		t.Error("not a favourite")
	}
	if rec := f.do(lan, "GET", "/web/movies?show=favorites", nil, c); !strings.Contains(rec.Body.String(), "/web/movies/"+splitID) ||
		strings.Contains(rec.Body.String(), "/web/movies/"+slaveID) {
		t.Error("favourites filter")
	}
}

func TestOwnAccount(t *testing.T) {
	f, c, _ := clientFixture(t)
	if rec := f.post("/web/account/password", c, url.Values{"current": {"wrong"}, "password": {"neu"}, "confirm": {"neu"}}); !strings.HasSuffix(rec.Header().Get("Location"), "err=currentWrong") {
		t.Errorf("wrong current password: %v", rec.Header())
	}
	rec := f.post("/web/account/password", c, url.Values{"current": {"pw-anna"}, "password": {"neu"}, "confirm": {"neu"}})
	if rec.Header().Get("Location") != "/web/login" {
		t.Errorf("password change: %v", rec.Header())
	}
	if _, c2 := f.login(lan, "anna", "neu"); c2 == nil {
		t.Error("new password does not work")
	}
}

func firstOr(m []map[string]store.Metadata) map[string]store.Metadata {
	if len(m) == 0 {
		return nil
	}
	return m[0]
}

func TestCrewAndCast(t *testing.T) {
	data, _ := json.Marshal(tmdb.Movie{ID: 1, Title: "12 Years a Slave", People: []tmdb.Person{
		{ID: 1, Name: "Steve McQueen", Type: "Director"}, {ID: 1, Name: "Steve McQueen", Type: "Writer"},
		{ID: 2, Name: "Chiwetel Ejiofor", Role: "Solomon Northup", Type: "Actor"},
	}})
	f, c, _ := clientFixture(t, map[string]store.Metadata{slaveID: {ItemID: slaveID, Status: store.MetaMatched, Data: string(data)}})
	body := f.do(lan, "GET", "/web/movies/"+slaveID, nil, c).Body.String()
	if strings.Count(body, "Steve McQueen") != 1 || !strings.Contains(body, "Regie, Drehbuch") || !strings.Contains(body, "Solomon Northup") {
		t.Errorf("people: %s", body)
	}
}

func TestRemuxPlayback(t *testing.T) {
	f, c, _ := clientFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "remux.mkv")
	os.WriteFile(path, []byte("x"), 0o644)
	// h264 with AC3 as the default audio, in Matroska: plays only remuxed.
	probe := strings.Replace(probeFixture(t, "twelve-years"), `"format_name": "mov,mp4,m4a,3gp,3g2,mj2"`, `"format_name": "matroska,webm"`, 1)
	items := []store.Item{{ID: remuxID, LibraryID: libID, Type: "Movie", Title: "Remux", SortTitle: "remux", Year: 2020,
		DateCreated: time.Now(), Files: []store.MediaFile{{ID: "f9", ItemID: remuxID, Path: path, Size: 1, Probe: probe}}}}
	srv := f.server
	srv.Catalog.Set(catalog.Build([]store.Library{{ID: libID, Name: "Filme", CollectionType: "movies"}}, items, catalog.Options{Locale: "de"}))
	srv.HLS = &hls.Manager{FFmpeg: "/nonexistent/ffmpeg", Dir: filepath.Join(dir, "hls"), Workers: 1,
		Keyframes: func(context.Context, hls.Source) ([]time.Duration, error) {
			return []time.Duration{0, 6 * time.Second, 12 * time.Second}, nil
		}}

	rec := f.do(lan, "GET", "/web/movies/"+remuxID, nil, c)
	if !strings.Contains(rec.Body.String(), "/web/play/"+remuxID) || !strings.Contains(rec.Body.String(), "wandelt den Ton") {
		t.Errorf("details: %s", rec.Body.String())
	}
	cfg := playerConfig(t, f.do(lan, "GET", "/web/play/"+remuxID, nil, c).Body.String())
	part := cfg["parts"].([]any)[0].(map[string]any)
	if part["direct"] != "" || part["hls"] != "/web/hls/f9/" {
		t.Errorf("part: %v", part)
	}
	rec = f.do(lan, "GET", "/web/hls/f9/1/index.m3u8", nil, c)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" ||
		!strings.Contains(rec.Body.String(), "#EXTINF:6.000000,\n0.ts\n") || !strings.Contains(rec.Body.String(), "2.ts") {
		t.Errorf("playlist: %d %q", rec.Code, rec.Body.String())
	}
	for path, want := range map[string]int{
		"/web/hls/f9/0/index.m3u8": http.StatusNotFound, // the video, not audio
		"/web/hls/f9/2/index.m3u8": http.StatusOK,       // another audio track
		"/web/hls/f9/1/x.ts":       http.StatusNotFound,
	} {
		if rec := f.do(lan, "GET", path, nil, c); rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	if rec := f.do(lan, "GET", "/web/hls/f9/1/index.m3u8", nil, nil); rec.Code != http.StatusFound {
		t.Errorf("playlist without session: %d", rec.Code)
	}

	// An account without conversion gets the original files only.
	if err := f.db.SetConversion(context.Background(), f.user("anna").ID, store.ConversionNone); err != nil {
		t.Fatal(err)
	}
	rec = f.do(lan, "GET", "/web/movies/"+remuxID, nil, c)
	if strings.Contains(rec.Body.String(), "/web/play/"+remuxID) || !strings.Contains(rec.Body.String(), "nur so, wie sie sind") {
		t.Errorf("details without conversion: %s", rec.Body.String())
	}
	if rec := f.do(lan, "GET", "/web/play/"+remuxID, nil, c); strings.Contains(rec.Body.String(), "/web/hls/") {
		t.Errorf("player without conversion: %s", rec.Body.String())
	}
	for _, path := range []string{"/web/hls/f9/1/index.m3u8", "/web/hls/f9/1/0.ts"} {
		if rec := f.do(lan, "GET", path, nil, c); rec.Code != http.StatusForbidden {
			t.Errorf("%s without conversion: %d", path, rec.Code)
		}
	}
}

// A movie that plays as it is keeps its direct play without conversion,
// but no remux of its other audio tracks.
func TestDirectPlayWithoutConversion(t *testing.T) {
	f, c, _ := clientFixture(t)
	f.server.HLS = &hls.Manager{FFmpeg: "/nonexistent/ffmpeg", Dir: t.TempDir(), Workers: 1}
	if err := f.db.SetConversion(context.Background(), f.user("anna").ID, store.ConversionNone); err != nil {
		t.Fatal(err)
	}
	cfg := playerConfig(t, f.do(lan, "GET", "/web/play/"+slaveID, nil, c).Body.String())
	part := cfg["parts"].([]any)[0].(map[string]any)
	if part["direct"] != "/web/stream/f1" || part["hls"] != "" {
		t.Errorf("part without conversion: %v", part)
	}
}
