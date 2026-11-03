package jellyfin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRouter() *Router {
	rt := &Router{}
	echo := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, name+" id="+r.PathValue("id")+" c="+r.PathValue("container"))
		}
	}
	rt.HandleFunc(http.MethodGet, "/Items/{id}", echo("item"))
	rt.HandleFunc(http.MethodGet, "/Items/Latest", echo("latest"))
	rt.HandleFunc(http.MethodGet, "/Videos/{id}/stream", echo("stream"))
	rt.HandleFunc(http.MethodGet, "/Videos/{id}/stream.{container}", echo("stream-ext"))
	rt.HandleFunc(http.MethodPost, "/System/Ping", echo("ping-post"))
	return rt
}

func TestRouterMatching(t *testing.T) {
	rt := newTestRouter()
	tests := []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/Items/abc", 200, "item id=abc c="},
		{"GET", "/items/ABC/", 200, "item id=ABC c="},
		{"GET", "/Items/Latest", 200, "latest id= c="},
		{"GET", "/ITEMS/latest", 200, "latest id= c="},
		{"GET", "/emby/Items/abc", 200, "item id=abc c="},
		{"GET", "/MediaBrowser//Items/abc", 200, "item id=abc c="},
		{"GET", "/Videos/v1/stream", 200, "stream id=v1 c="},
		{"GET", "/Videos/v1/Stream.MKV", 200, "stream-ext id=v1 c=MKV"},
		{"HEAD", "/Videos/v1/stream.mp4", 200, ""},
		{"POST", "/System/Ping", 200, "ping-post id= c="},
		{"GET", "/System/Ping", 405, ""},
		{"GET", "/Videos/v1/stream.", 404, ""},
		{"GET", "/Nope", 404, ""},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			if tc.status == 200 && tc.method != "HEAD" && rec.Body.String() != tc.body {
				t.Errorf("body %q, want %q", rec.Body.String(), tc.body)
			}
		})
	}
}

func TestRouterMethodNotAllowedListsMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest("DELETE", "/System/Ping", nil))
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want POST", got)
	}
}

func TestRouterRejectsBadPattern(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want panic for malformed pattern")
		}
	}()
	(&Router{}).HandleFunc("GET", "/a/{x}{y}", nil)
}

// Literal segments must win regardless of the order routes of other
// lengths were registered in.
func TestRouterLiteralWinsAcrossLengths(t *testing.T) {
	rt := &Router{}
	echo := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name) }
	}
	rt.HandleFunc(http.MethodGet, "/Items/{id}", echo("item"))
	rt.HandleFunc(http.MethodGet, "/Items", echo("items"))
	rt.HandleFunc(http.MethodGet, "/Items/Latest", echo("latest"))
	for path, want := range map[string]string{"/Items/Latest": "latest", "/Items/x": "item", "/Items": "items"} {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Body.String() != want {
			t.Errorf("%s: %q, want %q", path, rec.Body.String(), want)
		}
	}
}
