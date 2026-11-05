package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"

	"homeflix/internal/auth"
	"homeflix/internal/clientaddr"
	"homeflix/internal/lockout"
	"homeflix/internal/store"
)

func TestMain(m *testing.M) {
	auth.HashParams = auth.Params{MemoryKiB: 1024, Time: 1, Threads: 1}
	os.Exit(m.Run())
}

// sender is where a test request comes from.
type sender struct {
	remote string
	header http.Header
}

var (
	lan = sender{remote: "192.168.1.20:5000"}
	// tailscale serve on the same host.
	tailnet = sender{remote: "127.0.0.1:40000", header: http.Header{"X-Forwarded-For": {"100.101.102.103"},
		"X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"homeflix.example.ts.net"}}}
	// Caddy, with a client on the internet.
	internet = sender{remote: "192.168.1.2:40000", header: http.Header{"X-Forwarded-For": {"203.0.113.9"},
		"X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"kino.example.de"}}}
)

func (s sender) origin() string {
	if h := s.header.Get("X-Forwarded-Host"); h != "" {
		return "https://" + h
	}
	return "http://example.com"
}

type fixture struct {
	t    *testing.T
	h    http.Handler
	db   *store.Store
	logs *bytes.Buffer
}

// newFixture serves the pages; setup, if given, may fill in more of the
// server before its handler is made.
func newFixture(t *testing.T, setup ...func(*Server)) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, u := range []struct {
		name  string
		admin bool
	}{{"anna", false}, {"admin", true}} {
		hash, _ := auth.HashPassword("pw-" + u.name)
		if _, err := db.CreateUser(context.Background(), u.name, hash, u.admin); err != nil {
			t.Fatal(err)
		}
	}
	logs := &bytes.Buffer{}
	s := &Server{ServerName: "Kino", Store: db, Log: slog.New(slog.NewTextHandler(logs, nil)), Logins: lockout.NewGuard(),
		AdminFrom: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("100.64.0.0/10")},
		Language:  "de"}
	for _, f := range setup {
		f(s)
	}
	res := clientaddr.Resolver{Trusted: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("192.168.1.2/32")}}
	return &fixture{t: t, h: res.Middleware(s.Handler()), db: db, logs: logs}
}

// do sends a request; form values make it a POST with the sender's Origin.
func (f *fixture) do(from sender, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://example.com"+path, body)
	req.RemoteAddr = from.remote
	for k, v := range from.header {
		req.Header[k] = v
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", from.origin())
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) login(from sender, name, password string) (*httptest.ResponseRecorder, *http.Cookie) {
	f.t.Helper()
	rec := f.do(from, "POST", "/web/login", url.Values{"username": {name}, "password": {password}, "next": {"/web/"}}, nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return rec, c
		}
	}
	return rec, nil
}

func TestLoginAndLogout(t *testing.T) {
	f := newFixture(t)
	rec := f.do(lan, "GET", "/web/", nil, nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/web/login?next=%2Fweb%2F" {
		t.Fatalf("signed out: %d %v", rec.Code, rec.Header())
	}
	rec = f.do(lan, "GET", "/web/login", nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Anmelden") ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("login page: %d %v", rec.Code, rec.Header())
	}

	if rec, c := f.login(lan, "anna", "wrong"); rec.Code != http.StatusUnauthorized || c != nil ||
		!strings.Contains(rec.Body.String(), "Benutzername oder Passwort falsch") {
		t.Errorf("wrong password: %d %q", rec.Code, rec.Body.String())
	}
	rec, c := f.login(lan, "anna", "pw-anna")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/web/" || c == nil {
		t.Fatalf("login: %d %v", rec.Code, rec.Header())
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure {
		t.Errorf("cookie over plain HTTP: %+v", c)
	}
	rec = f.do(lan, "GET", "/web/", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Die Bibliothek ist leer") || strings.Contains(rec.Body.String(), "/admin/") {
		t.Errorf("home: %d %q", rec.Code, rec.Body.String())
	}
	// The web session is a device of the user, like a Moonfin app.
	user, _ := f.db.UserByName(context.Background(), "anna")
	toks, _ := f.db.Tokens(context.Background(), user.ID)
	if len(toks) != 1 || toks[0].Client != clientName || toks[0].RemoteAddr != "192.168.1.20" {
		t.Errorf("tokens: %+v", toks)
	}

	if rec := f.do(lan, "POST", "/web/logout", url.Values{}, c); rec.Code != http.StatusForbidden {
		t.Errorf("logout without form token: %d", rec.Code)
	}
	csrf := (&session{raw: c.Value}).CSRF()
	rec = f.do(lan, "POST", "/web/logout", url.Values{"csrf": {csrf}}, c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("logout: %d", rec.Code)
	}
	if rec := f.do(lan, "GET", "/web/", nil, c); rec.Code != http.StatusFound {
		t.Errorf("after logout: %d", rec.Code)
	}
}

func TestCookieOverHTTPS(t *testing.T) {
	f := newFixture(t)
	if _, c := f.login(tailnet, "anna", "pw-anna"); c == nil || !c.Secure {
		t.Errorf("cookie through tailscale serve: %+v", c)
	}
}

func TestCrossSiteRequests(t *testing.T) {
	f := newFixture(t)
	form := strings.NewReader(url.Values{"username": {"anna"}, "password": {"pw-anna"}}.Encode())
	for name, origin := range map[string]string{"other site": "https://evil.example", "no origin": ""} {
		req := httptest.NewRequest("POST", "http://example.com/web/login", form)
		req.RemoteAddr = lan.remote
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	// A Referer of this server counts when the browser sends no Origin.
	req := httptest.NewRequest("POST", "http://example.com/web/login",
		strings.NewReader(url.Values{"username": {"anna"}, "password": {"pw-anna"}}.Encode()))
	req.RemoteAddr = lan.remote
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "http://example.com/web/login?next=%2Fweb%2F")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("same-site referer: %d", rec.Code)
	}
}

func TestNextPage(t *testing.T) {
	for in, want := range map[string]string{"/web/x": "/web/x", "/admin/": "/admin/", "//evil.example/": "/web/",
		"https://evil.example/": "/web/", "/web/\\evil": "/web/", "": "/web/", "/System/Info": "/web/"} {
		if got := nextPage(in); got != want {
			t.Errorf("nextPage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdminAccess(t *testing.T) {
	f := newFixture(t)
	_, admin := f.login(tailnet, "admin", "pw-admin")
	rec := f.do(tailnet, "GET", "/admin/", nil, admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "100.101.102.103") ||
		!strings.Contains(rec.Body.String(), "https://homeflix.example.ts.net") {
		t.Errorf("admin over the tailnet: %d %q", rec.Code, rec.Body.String())
	}
	if rec := f.do(tailnet, "GET", "/web/", nil, admin); !strings.Contains(rec.Body.String(), `href="/admin/"`) {
		t.Error("home lacks the admin link for an admin on the tailnet")
	}
	// The same session through Caddy from the internet: refused.
	rec = f.do(internet, "GET", "/admin/", nil, admin)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "nur aus dem Heimnetz und dem Tailnet") {
		t.Errorf("admin through Caddy: %d %q", rec.Code, rec.Body.String())
	}
	if rec := f.do(internet, "GET", "/web/", nil, admin); strings.Contains(rec.Body.String(), `href="/admin/"`) {
		t.Error("admin link shown outside the admin networks")
	}
	if !strings.Contains(f.logs.String(), "admin UI refused") {
		t.Error("refusal not logged")
	}
	_, anna := f.login(lan, "anna", "pw-anna")
	if rec := f.do(lan, "GET", "/admin/", nil, anna); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "nur für Administratoren") {
		t.Errorf("admin UI for a user: %d", rec.Code)
	}
}

func TestLockout(t *testing.T) {
	f := newFixture(t)
	for range 10 {
		f.login(internet, "anna", "wrong")
	}
	rec, c := f.login(internet, "anna", "pw-anna")
	if rec.Code != http.StatusTooManyRequests || c != nil || rec.Header().Get("Retry-After") == "" {
		t.Errorf("after 10 failures: %d %v", rec.Code, rec.Header())
	}
	if _, c := f.login(tailnet, "anna", "pw-anna"); c == nil {
		t.Error("other address locked out too")
	}
}

func TestStatic(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(lan, "GET", "/favicon.ico", nil, nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("favicon: %d %v", rec.Code, rec.Header())
	}
	rec := f.do(lan, "GET", asset("style.css"), nil, nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || len(assetVersions["style.css"]) != 12 {
		t.Errorf("stylesheet: %d %v", rec.Code, rec.Header())
	}
	if rec := f.do(lan, "GET", "/web/static/style.css", nil, nil); strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Error("unversioned stylesheet cached for good")
	}
}

func TestDeviceName(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15": "Safari on macOS",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1":   "Safari on iPhone",
		"Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0":                                                "Firefox on Linux",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 Edg/140.0":                     "Edge on Windows",
		"curl/8.7.1": "Browser",
	} {
		if got := deviceName(ua); got != want {
			t.Errorf("%s: %q, want %q", ua, got, want)
		}
	}
}
