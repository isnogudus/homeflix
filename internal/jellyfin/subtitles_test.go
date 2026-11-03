package jellyfin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
	"homeflix/internal/subtitles"
)

func TestSubtitleStream(t *testing.T) {
	s := newTestServer(t, nil)
	dir := t.TempDir()
	// The probe data of "12 Years a Slave" with a SubRip track added.
	var probe map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "twelve-years")), &probe); err != nil {
		t.Fatal(err)
	}
	probe["streams"] = append(probe["streams"].([]any), map[string]any{
		"index": 4, "codec_type": "subtitle", "codec_name": "subrip", "tags": map[string]any{"language": "ger"},
	})
	b, _ := json.Marshal(probe)
	path := filepath.Join(dir, "Alpha.mkv")
	os.WriteFile(path, []byte("x"), 0o644)
	items := []store.Item{{ID: movieA, LibraryID: libID, Type: "Movie", Title: "Alpha", SortTitle: "alpha",
		DateCreated: time.Now(), Files: []store.MediaFile{{ID: fileA, ItemID: movieA, Path: path, Size: 1, Probe: string(b)}}}}
	s.Catalog.Set(catalog.Build([]store.Library{{ID: libID, Name: "Filme", CollectionType: "movies"}}, items, catalog.Options{}))
	bin := filepath.Join(dir, "ffmpeg")
	os.WriteFile(bin, []byte("#!/bin/sh\nfor a; do last=$a; done\nprintf '1\\n00:00:01,000 --> 00:00:02,000\\nHallo\\n' > \"$last\"\n"), 0o755)
	s.Subtitles = &subtitles.Service{FFmpeg: bin, Dir: filepath.Join(dir, "subtitles")}
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	auth := "?ApiKey=" + c.token
	for _, p := range []string{
		"/Videos/" + movieA + "/" + movieA + "/Subtitles/4/0/Stream.srt",
		"/videos/" + movieA + "/" + movieA + "/subtitles/4/Stream.SRT",
	} {
		rec := get(p + auth)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/x-subrip") ||
			!strings.Contains(rec.Body.String(), "Hallo") {
			t.Errorf("%s: %d %v %q", p, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	for p, want := range map[string]int{
		"/Videos/" + movieA + "/" + movieA + "/Subtitles/4/Stream.srt":             http.StatusUnauthorized,
		"/Videos/" + movieA + "/" + movieA + "/Subtitles/1/Stream.srt" + auth:      http.StatusNotFound, // audio
		"/Videos/" + movieA + "/" + movieA + "/Subtitles/4/Stream.sub" + auth:      http.StatusNotFound,
		"/Videos/ffffffffffffffffffffffffffffffff/x/Subtitles/4/Stream.srt" + auth: http.StatusNotFound,
		"/Videos/" + movieA + "/" + movieA + "/Subtitles/x/Stream.srt" + auth:      http.StatusNotFound,
	} {
		if rec := get(p); rec.Code != want {
			t.Errorf("%s: %d, want %d", p, rec.Code, want)
		}
	}
}
