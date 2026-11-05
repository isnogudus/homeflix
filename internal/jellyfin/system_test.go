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
	"homeflix/internal/quickconnect"
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
	req.Host = "192.168.1.10:8096"
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
			"LocalAddress":           "http://192.168.1.10:8096",
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
		if rec := get(t, h, "/Packages?ApiKey=secret", nil); rec.Code != http.StatusNotFound {
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

func TestQuickConnectDisabled(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	if rec := get(t, h, "/QuickConnect/Enabled", nil); strings.TrimSpace(rec.Body.String()) != "false" {
		t.Errorf("enabled: %q", rec.Body.String())
	}
	for _, tc := range []struct{ method, path string }{
		{"POST", "/QuickConnect/Initiate"}, {"GET", "/QuickConnect/Connect?Secret=x"}, {"POST", "/Users/AuthenticateWithQuickConnect"},
		{"POST", "/QuickConnect/Authorize?code=123456"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Quick connect is disabled") {
			t.Errorf("%s %s: %d %q", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestQuickConnect(t *testing.T) {
	s := newTestServer(t, nil)
	s.QuickConnect = &quickconnect.Service{}
	anna := addUser(t, s, "anna", "pw", false)
	addUser(t, s, "admin", "pw", true)
	h := s.Handler()
	if rec := get(t, h, "/QuickConnect/Enabled", nil); strings.TrimSpace(rec.Body.String()) != "true" {
		t.Errorf("enabled: %q", rec.Body.String())
	}

	// The app asks for a code and polls.
	app := &apiClient{t: t, h: h}
	q := decode[QuickConnectResult](t, app.do("POST", "/QuickConnect/Initiate", "", nil))
	if len(q.Code) != 6 || q.Secret == "" || q.Authenticated || q.AppName != "Moonfin" {
		t.Fatalf("initiate: %+v", q)
	}
	if rec := app.do("GET", "/QuickConnect/Connect?secret=nope", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown secret: %d", rec.Code)
	}
	if rec := app.do("POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+q.Secret+`"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("token before confirmation: %d", rec.Code)
	}

	// Anna confirms it from another client; she may not confirm for others.
	phone := &apiClient{t: t, h: h}
	login(t, phone, "anna", "pw")
	if rec := phone.do("POST", "/QuickConnect/Authorize?code=000000", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("wrong code: %d", rec.Code)
	}
	adminID := ""
	if u, err := s.Store.UserByName(t.Context(), "admin"); err == nil {
		adminID = u.ID
	}
	if rec := phone.do("POST", "/QuickConnect/Authorize?code="+q.Code+"&userId="+adminID, "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("for another user: %d", rec.Code)
	}
	if rec := phone.do("POST", "/QuickConnect/Authorize?code="+q.Code, "", nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "true" {
		t.Fatalf("authorize: %d %q", rec.Code, rec.Body.String())
	}
	if st := decode[QuickConnectResult](t, app.do("GET", "/QuickConnect/Connect?Secret="+q.Secret, "", nil)); !st.Authenticated {
		t.Errorf("status after confirmation: %+v", st)
	}
	res := decode[AuthenticationResult](t, app.do("POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+q.Secret+`"}`, nil))
	if res.AccessToken == "" || normalizeID(res.User.ID) != anna.ID {
		t.Fatalf("authenticate: %+v", res)
	}
	app.token = res.AccessToken
	if rec := app.do("GET", "/Users/Me", "", nil); rec.Code != 200 {
		t.Errorf("token of quick connect: %d", rec.Code)
	}
	if rec := app.do("POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+q.Secret+`"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("secret used twice: %d", rec.Code)
	}
}

func TestEncodingConfigurationAndPlugins(t *testing.T) {
	s := newTestServer(t, nil)
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")
	enc := decode[map[string]any](t, c.do("GET", "/System/Configuration/encoding", "", nil))
	if enc["HardwareAccelerationType"] != "none" || enc["EnableHardwareEncoding"] != false {
		t.Errorf("encoding: %v", enc)
	}
	if rec := c.do("GET", "/System/Configuration/network", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("other section: %d", rec.Code)
	}
	if rec := c.do("GET", "/Plugins", "", nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("plugins: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, s.Handler(), "/Plugins", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("plugins without token: %d", rec.Code)
	}
}

func TestPluginProbesAreQuiet(t *testing.T) {
	var logs bytes.Buffer
	h := newTestServer(t, &logs).Handler()
	for _, p := range pluginProbes {
		if rec := get(t, h, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if strings.Contains(logs.String(), "unsupported endpoint") {
		t.Errorf("plugin probes logged as unsupported:\n%s", logs.String())
	}
}
