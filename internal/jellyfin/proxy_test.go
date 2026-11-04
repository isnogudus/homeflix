package jellyfin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"homeflix/internal/clientaddr"
	"homeflix/internal/lockout"
)

// via sends a request as if it came from remote, with forwarded headers.
func via(t *testing.T, h http.Handler, method, path, body, remote string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestForwardedClients(t *testing.T) {
	s := newTestServer(t, nil)
	s.Clients = clientaddr.Resolver{Trusted: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("192.168.1.2/32")}}
	s.AdminFrom = []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("100.64.0.0/10")}
	s.RequestScan = func() {}
	addUser(t, s, "admin", "a", true)
	h := s.Handler()
	tailnet := http.Header{"X-Forwarded-For": {"100.101.102.103"}, "X-Forwarded-Proto": {"https"},
		"X-Forwarded-Host": {"homeflix.example.ts.net"}, "Authorization": {moonfinHeader}}
	internet := http.Header{"X-Forwarded-For": {"100.64.1.1, 203.0.113.9"}, "X-Forwarded-Proto": {"https"},
		"X-Forwarded-Host": {"kino.example.de"}, "Authorization": {moonfinHeader}}

	info := decode[PublicSystemInfo](t, via(t, h, "GET", "/System/Info/Public", "", "127.0.0.1:40000", tailnet))
	if info.LocalAddress != "https://homeflix.example.ts.net" {
		t.Errorf("local address through tailscale serve: %q", info.LocalAddress)
	}
	res := decode[AuthenticationResult](t, via(t, h, "POST", "/Users/AuthenticateByName",
		`{"Username":"admin","Pw":"a"}`, "127.0.0.1:40000", tailnet))
	if res.SessionInfo.RemoteEndPoint != "100.101.102.103" {
		t.Errorf("session address: %q", res.SessionInfo.RemoteEndPoint)
	}

	// The same admin may scan over the tailnet, not through Caddy from outside.
	auth := func(hdr http.Header) http.Header {
		out := hdr.Clone()
		out.Set("Authorization", moonfinHeader+`, Token="`+res.AccessToken+`"`)
		return out
	}
	if rec := via(t, h, "POST", "/Library/Refresh", "", "127.0.0.1:40000", auth(tailnet)); rec.Code != http.StatusNoContent {
		t.Errorf("scan over the tailnet: %d", rec.Code)
	}
	if rec := via(t, h, "POST", "/Library/Refresh", "", "192.168.1.2:40000", auth(internet)); rec.Code != http.StatusForbidden {
		t.Errorf("scan through Caddy from outside: %d", rec.Code)
	}
	// Forged headers from an untrusted sender do not make it a tailnet client.
	if rec := via(t, h, "POST", "/Library/Refresh", "", "203.0.113.9:40000", auth(tailnet)); rec.Code != http.StatusForbidden {
		t.Errorf("scan with forged headers: %d", rec.Code)
	}
}

func TestLoginLockout(t *testing.T) {
	s := newTestServer(t, nil)
	s.Logins = lockout.NewGuard()
	addUser(t, s, "anna", "pw", false)
	h := s.Handler()
	hdr := http.Header{"Authorization": {moonfinHeader}}
	for i := range 10 {
		if rec := via(t, h, "POST", "/Users/AuthenticateByName", `{"Username":"anna","Pw":"wrong"}`, "203.0.113.9:1", hdr); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, rec.Code)
		}
	}
	rec := via(t, h, "POST", "/Users/AuthenticateByName", `{"Username":"anna","Pw":"pw"}`, "203.0.113.9:1", hdr)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("after 10 failures: %d %v", rec.Code, rec.Header())
	}
	// Another address may still sign in.
	if rec := via(t, h, "POST", "/Users/AuthenticateByName", `{"Username":"anna","Pw":"pw"}`, "198.51.100.1:1", hdr); rec.Code != http.StatusOK {
		t.Errorf("other address: %d", rec.Code)
	}
}

func TestWebPaths(t *testing.T) {
	s := newTestServer(t, nil)
	s.Web = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "web "+r.URL.Path) })
	h := s.Handler()
	for path, want := range map[string]string{"/web/": "web /web/", "/web/login": "web /web/login", "/admin/": "web /admin/",
		"/Admin": "web /Admin"} {
		if rec := via(t, h, "GET", path, "", "192.168.1.20:1", nil); rec.Body.String() != want {
			t.Errorf("%s: %q", path, rec.Body.String())
		}
	}
	if rec := via(t, h, "GET", "/", "", "192.168.1.20:1", nil); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/web/" {
		t.Errorf("root: %d %v", rec.Code, rec.Header())
	}
	if rec := via(t, h, "GET", "/System/Ping", "", "192.168.1.20:1", nil); rec.Code != http.StatusOK {
		t.Errorf("API next to the web client: %d", rec.Code)
	}
}
