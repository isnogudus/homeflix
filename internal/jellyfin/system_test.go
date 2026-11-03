package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
)

func newTestServer(t *testing.T, logBuf *bytes.Buffer) *Server {
	t.Helper()
	if logBuf == nil {
		logBuf = &bytes.Buffer{}
	}
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{
		ServerID:   "0123456789abcdef0123456789abcdef",
		ServerName: "testhost",
		Store:      db,
		Catalog:    &catalog.Catalog{Store: db},
		Log:        slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func get(t *testing.T, h http.Handler, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "192.168.21.15:8096"
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPublicSystemInfo(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	for _, path := range []string{"/System/Info/Public", "/emby/System/Info/Public"} {
		rec := get(t, h, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: Content-Type %q", path, ct)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"LocalAddress":           "http://192.168.21.15:8096",
			"ServerName":             "testhost",
			"Version":                CompatVersion,
			"ProductName":            "Jellyfin Server",
			"OperatingSystem":        "",
			"Id":                     "0123456789abcdef0123456789abcdef",
			"StartupWizardCompleted": true,
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %v, want %v", path, k, got[k], v)
			}
		}
	}
}

func TestPing(t *testing.T) {
	rec := get(t, newTestServer(t, nil).Handler(), "/System/Ping", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `"Jellyfin Server"` {
		t.Errorf("ping: %d %q", rec.Code, rec.Body.String())
	}
}

func TestPublicUsersIsEmptyList(t *testing.T) {
	rec := get(t, newTestServer(t, nil).Handler(), "/Users/Public", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("users/public: %d %q", rec.Code, rec.Body.String())
	}
}

func TestUnknownEndpointLoggedOnceWithoutQuery(t *testing.T) {
	var logs bytes.Buffer
	h := newTestServer(t, &logs).Handler()
	for range 3 {
		if rec := get(t, h, "/Plugins?ApiKey=secret", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("status %d", rec.Code)
		}
	}
	if n := strings.Count(logs.String(), "unsupported endpoint"); n != 1 {
		t.Errorf("unsupported endpoint logged %d times, want 1:\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "secret") {
		t.Errorf("log contains the query token:\n%s", logs.String())
	}
}

func TestRequestLogNamesClient(t *testing.T) {
	var logs bytes.Buffer
	h := newTestServer(t, &logs).Handler()
	get(t, h, "/System/Info/Public", http.Header{
		"Authorization": {`MediaBrowser Client="Moonfin", Device="iMac", DeviceId="d1", Version="3.0.0"`},
	})
	if !strings.Contains(logs.String(), "client=Moonfin") {
		t.Errorf("request log lacks client name:\n%s", logs.String())
	}
}

func TestPanicBecomes500(t *testing.T) {
	var logs bytes.Buffer
	s := newTestServer(t, &logs)
	h := s.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := get(t, h, "/x", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), "boom") {
		t.Errorf("status %d, logs %q", rec.Code, logs.String())
	}
}
