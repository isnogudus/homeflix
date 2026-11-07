package web

import (
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
	"homeflix/internal/media"
	"homeflix/internal/store"
)

const (
	musicLib = "21111111111111111111111111111111"
	bookLib  = "31111111111111111111111111111111"
	trackMP3 = "a3000000000000000000000000000001"
	trackWMA = "a3000000000000000000000000000002"
	bookID   = "b3000000000000000000000000000001"
)

func musicFixture(t *testing.T) (*fixture, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	item := func(id, lib, typ, name string, m media.AudioMeta, probe string) store.Item {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("audio "+name), 0o644)
		m.Cover = -1
		b, _ := json.Marshal(m)
		return store.Item{ID: id, LibraryID: lib, Type: typ, Path: p, Title: name, SortTitle: name, DateCreated: time.Now(),
			Files: []store.MediaFile{{ID: "f" + id, ItemID: id, Path: p, Size: 10, Duration: 3 * time.Minute,
				AudioMeta: string(b), Probe: probe}}}
	}
	items := []store.Item{
		item(trackMP3, musicLib, "Audio", "01.mp3", media.AudioMeta{Title: "Rosanna", Artist: "Toto", AlbumArtist: "Toto",
			Album: "IV", Track: 1, Codec: "mp3", Container: "mp3"}, ""),
		item(trackWMA, musicLib, "Audio", "02.wma", media.AudioMeta{Title: "Africa", Artist: "Toto", AlbumArtist: "Toto",
			Album: "IV", Track: 2, Codec: "wmav2", Container: "asf"}, ""),
		item(bookID, bookLib, "AudioBook", "Blackout.m4b", media.AudioMeta{Album: "Blackout", AlbumArtist: "Marc Elsberg"},
			`{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac"}],"chapters":[{"start_time":"0","tags":{"title":"Eins"}},`+
				`{"start_time":"90","tags":{"title":"Zwei"}}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"180"}}`),
	}
	f := newFixture(t, func(s *Server) {
		s.Catalog = &catalog.Catalog{Store: s.Store}
		s.Catalog.Set(catalog.Build([]store.Library{{ID: musicLib, Name: "Musik", CollectionType: "music"},
			{ID: bookLib, Name: "Hörbücher", CollectionType: "books"}}, items, catalog.Options{Locale: "de"}))
		s.HLS = &hls.Manager{FFmpeg: "ffmpeg", Dir: t.TempDir(), Workers: 1}
	})
	_, c := f.login(lan, "anna", "pw-anna")
	return f, c
}

var queueAttr = regexp.MustCompile(`data-queue="([^"]*)"`)

func queueOf(t *testing.T, body string) []audioTrack {
	t.Helper()
	m := queueAttr.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no queue in page")
	}
	var q []audioTrack
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &q); err != nil {
		t.Fatalf("queue %q: %v", m[1], err)
	}
	return q
}

func TestMusicPages(t *testing.T) {
	f, c := musicFixture(t)
	snap := f.server.Catalog.Snapshot()
	al := snap.Albums()[0]
	for _, p := range []string{"/web/", "/web/music", "/web/music/artists", "/web/music/playlists", "/web/books",
		"/web/music/albums/" + al.ID, "/web/music/artists/" + al.Artists[0].ID, "/web/books/" + bookID, "/web/search?q=toto"} {
		rec := f.do(lan, "GET", p, nil, c)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="audioplayer"`) {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	body := f.do(lan, "GET", "/web/music/albums/"+al.ID, nil, c).Body.String()
	q := queueOf(t, body)
	if len(q) != 2 || q[0].Title != "Rosanna" || q[0].Direct != "/web/stream/f"+trackMP3 || q[0].MIME != "audio/mpeg" ||
		q[1].Direct != "" || q[1].HLS != "/web/audio/"+trackWMA+"/index.m3u8" {
		t.Errorf("album queue: %+v", q)
	}
	if !strings.Contains(body, `href="/web/music">Musik</a>`) && !strings.Contains(body, `href="/web/music" aria-current="page">Musik</a>`) {
		t.Error("no music link in the navigation")
	}
	book := queueOf(t, f.do(lan, "GET", "/web/books/"+bookID, nil, c).Body.String())
	if len(book) != 1 || !book[0].Book || len(book[0].Chapters) != 2 || book[0].Chapters[1].At != 90 {
		t.Errorf("book queue: %+v", book)
	}
	// Playlists: made from the album, listed, removed.
	rec := f.do(lan, "POST", "/web/music/playlists", url.Values{"csrf": {csrfOf(c)}, "name": {"Mix"}, "ids": {trackMP3 + "," + trackWMA}}, c)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/web/music/playlists/") {
		t.Fatalf("create playlist: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	pl := f.do(lan, "GET", rec.Header().Get("Location"), nil, c).Body.String()
	if q := queueOf(t, pl); len(q) != 2 {
		t.Errorf("playlist queue: %+v", q)
	}
	// Favourites of albums return to the album.
	rec = f.do(lan, "POST", "/web/favorite/"+al.ID, url.Values{"csrf": {csrfOf(c)}, "favorite": {"on"},
		"back": {"/web/music/albums/" + al.ID}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/web/music/albums/"+al.ID {
		t.Errorf("favourite: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := f.do(lan, "POST", "/web/favorite/"+al.ID, url.Values{"csrf": {csrfOf(c)}, "back": {"//evil.example/"}}, c); rec.Header().Get("Location") != "/web/music" {
		t.Errorf("foreign back link followed: %s", rec.Header().Get("Location"))
	}
	// Tracks are not movies.
	if rec := f.do(lan, "GET", "/web/movies/"+trackMP3, nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("track as movie: %d", rec.Code)
	}
	// Progress of a book follows the audiobook rules.
	rec = f.do(lan, "POST", "/web/progress", url.Values{"csrf": {csrfOf(c)}, "item": {bookID}, "source": {bookID},
		"event": {"stop"}, "position": {"100"}}, c)
	if rec.Code != http.StatusNoContent {
		t.Errorf("progress: %d", rec.Code)
	}
}
