package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"homeflix/internal/passkey"
	"homeflix/internal/passkey/passkeytest"
	"homeflix/internal/quickconnect"
)

func withPasskeys(t *testing.T) func(*Server) {
	return func(s *Server) {
		keys, err := passkey.New(s.Store, "Kino", []string{"https://homeflix.example.ts.net"})
		if err != nil {
			t.Fatal(err)
		}
		s.Passkeys = keys
	}
}

func decodeJSON[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func TestPasskeyPages(t *testing.T) {
	f := newFixture(t, withPasskeys(t))
	// The login page offers passkeys only on the configured origin.
	if body := f.do(tailnet, "GET", "/web/login", nil, nil).Body.String(); !strings.Contains(body, `id="passkeylogin"`) ||
		!strings.Contains(body, `autocomplete="username webauthn"`) || !strings.Contains(body, "passkey.js") {
		t.Errorf("tailnet login page without passkeys:\n%s", body)
	}
	for _, from := range []sender{lan, internet} {
		if body := f.do(from, "GET", "/web/login", nil, nil).Body.String(); strings.Contains(body, "passkey") {
			t.Errorf("passkeys offered on %s", from.origin())
		}
		if rec := f.do(from, "POST", "/web/passkey/begin", url.Values{}, nil); rec.Code != http.StatusNotFound {
			t.Errorf("begin on %s: %d", from.origin(), rec.Code)
		}
	}

	_, c := f.login(tailnet, "anna", "pw-anna")
	csrf := csrfOf(c)
	body := f.do(tailnet, "GET", "/web/account", nil, c).Body.String()
	if !strings.Contains(body, `id="passkeyadd"`) || !strings.Contains(body, "Noch keine Passkeys") {
		t.Errorf("account page on the tailnet:\n%s", body)
	}
	_, lc := f.login(lan, "anna", "pw-anna")
	if body := f.do(lan, "GET", "/web/account", nil, lc).Body.String(); strings.Contains(body, `id="passkeyadd"`) ||
		!strings.Contains(body, "https://homeflix.example.ts.net/web/account") {
		t.Errorf("account page on the LAN should point to the tailnet name:\n%s", body)
	}

	// Add a passkey.
	a := passkeytest.New(t)
	rec := f.do(tailnet, "POST", "/web/account/passkeys/begin", url.Values{"csrf": {csrf}}, c)
	if rec.Code != 200 {
		t.Fatalf("register begin: %d %s", rec.Code, rec.Body.String())
	}
	opts := decodeJSON[passkey.Options](t, rec.Body.String())
	if rec := f.do(tailnet, "POST", "/web/account/passkeys/begin", url.Values{}, c); rec.Code != http.StatusForbidden {
		t.Errorf("register without CSRF: %d", rec.Code)
	}
	rec = f.do(tailnet, "POST", "/web/account/passkeys/finish", url.Values{"csrf": {csrf}, "ceremony": {opts.Ceremony},
		"credential": {string(a.Create(opts.PublicKey, "https://homeflix.example.ts.net"))}, "name": {"iPhone"}}, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "passkeyAdded") {
		t.Fatalf("register finish: %d %s", rec.Code, rec.Body.String())
	}
	if body := f.do(tailnet, "GET", "/web/account", nil, c).Body.String(); !strings.Contains(body, "iPhone") ||
		!strings.Contains(body, "homeflix.example.ts.net (diese Adresse)") {
		t.Errorf("passkey not listed:\n%s", body)
	}

	// Sign in with it, without a cookie.
	rec = f.do(tailnet, "POST", "/web/passkey/begin", url.Values{"conditional": {"1"}}, nil)
	login := decodeJSON[passkey.Options](t, rec.Body.String())
	if login.Mediation != "conditional" {
		t.Errorf("mediation %q", login.Mediation)
	}
	rec = f.do(tailnet, "POST", "/web/passkey/finish", url.Values{"ceremony": {login.Ceremony},
		"credential": {string(a.Get(login.PublicKey, "https://homeflix.example.ts.net"))}, "next": {"/web/movies"}}, nil)
	if rec.Code != 200 || decodeJSON[map[string]string](t, rec.Body.String())["next"] != "/web/movies" {
		t.Fatalf("passkey sign-in: %d %s", rec.Code, rec.Body.String())
	}
	var pc *http.Cookie
	for _, k := range rec.Result().Cookies() {
		if k.Name == cookieName && k.Value != "" {
			pc = k
		}
	}
	if pc == nil || !pc.Secure {
		t.Fatalf("passkey sign-in cookie: %+v", pc)
	}
	if rec := f.do(tailnet, "GET", "/web/account", nil, pc); rec.Code != 200 || !strings.Contains(rec.Body.String(), "anna") {
		t.Errorf("session of the passkey: %d", rec.Code)
	}
	if !strings.Contains(f.logs.String(), "method=passkey") {
		t.Errorf("passkey login not logged:\n%s", f.logs.String())
	}

	// A forged answer counts as a failed login of the address.
	rec = f.do(tailnet, "POST", "/web/passkey/begin", url.Values{}, nil)
	login = decodeJSON[passkey.Options](t, rec.Body.String())
	rec = f.do(tailnet, "POST", "/web/passkey/finish", url.Values{"ceremony": {login.Ceremony},
		"credential": {string(a.Get(login.PublicKey, "https://evil.example"))}}, nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "nicht angenommen") {
		t.Errorf("forged answer: %d %s", rec.Code, rec.Body.String())
	}
	// Cross-site requests are refused.
	if rec := f.postFrom(tailnet, "/web/passkey/begin", "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site begin: %d", rec.Code)
	}

	// Remove it; the admin sees and removes passkeys too.
	id := base64.RawURLEncoding.EncodeToString(a.ID)
	if rec := f.do(tailnet, "POST", "/web/account/passkeys/"+id+"/delete", url.Values{"csrf": {csrf}}, c); rec.Code != http.StatusSeeOther ||
		!strings.Contains(rec.Header().Get("Location"), "passkeyRemoved") {
		t.Errorf("remove: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if keys, _ := f.db.Passkeys(t.Context(), userID(t, f, "anna")); len(keys) != 0 {
		t.Errorf("passkey left: %+v", keys)
	}
}

func TestAdminPasskeys(t *testing.T) {
	f := newFixture(t, withPasskeys(t))
	_, c := f.login(tailnet, "anna", "pw-anna")
	a := passkeytest.New(t)
	opts := decodeJSON[passkey.Options](t, f.do(tailnet, "POST", "/web/account/passkeys/begin", url.Values{"csrf": {csrfOf(c)}}, c).Body.String())
	f.do(tailnet, "POST", "/web/account/passkeys/finish", url.Values{"csrf": {csrfOf(c)}, "ceremony": {opts.Ceremony},
		"credential": {string(a.Create(opts.PublicKey, "https://homeflix.example.ts.net"))}, "name": {"Laptop"}}, c)

	_, ac := f.login(lan, "admin", "pw-admin")
	anna := userID(t, f, "anna")
	body := f.do(lan, "GET", "/admin/users/"+anna, nil, ac).Body.String()
	if !strings.Contains(body, "Laptop") || !strings.Contains(body, "homeflix.example.ts.net") || strings.Contains(body, "diese Adresse") {
		t.Errorf("admin user page:\n%s", body)
	}
	id := base64.RawURLEncoding.EncodeToString(a.ID)
	if rec := f.do(lan, "POST", "/admin/users/"+anna+"/passkeys/"+id+"/delete", url.Values{"csrf": {csrfOf(ac)}}, ac); rec.Code != http.StatusSeeOther {
		t.Errorf("admin remove: %d", rec.Code)
	}
	if keys, _ := f.db.Passkeys(t.Context(), anna); len(keys) != 0 {
		t.Errorf("passkey left: %+v", keys)
	}
}

func TestQuickConnectPage(t *testing.T) {
	qc := &quickconnect.Service{}
	f := newFixture(t, func(s *Server) { s.QuickConnect = qc })
	_, c := f.login(lan, "anna", "pw-anna")
	if body := f.do(lan, "GET", "/web/account", nil, c).Body.String(); !strings.Contains(body, "/web/account/quickconnect") ||
		!strings.Contains(body, "Passkeys sind auf diesem Server nicht eingerichtet") {
		t.Errorf("account page:\n%s", body)
	}
	q, err := qc.Initiate(quickconnect.Request{DeviceID: "d", DeviceName: "iMac", AppName: "Moonfin", AppVersion: "1", Remote: "x"})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.do(lan, "POST", "/web/account/quickconnect", url.Values{"csrf": {csrfOf(c)}, "code": {"999 999"}}, c)
	if !strings.Contains(rec.Header().Get("Location"), "codeUnknown") {
		t.Errorf("wrong code: %s", rec.Header().Get("Location"))
	}
	rec = f.do(lan, "POST", "/web/account/quickconnect", url.Values{"csrf": {csrfOf(c)}, "code": {q.Code[:3] + " " + q.Code[3:]}}, c)
	if !strings.Contains(rec.Header().Get("Location"), "codeConfirmed") {
		t.Errorf("code: %s", rec.Header().Get("Location"))
	}
	if st, _ := qc.Status(q.Secret); st.UserID != userID(t, f, "anna") {
		t.Errorf("not authorized for anna: %+v", st)
	}
	off := newFixture(t)
	_, oc := off.login(lan, "anna", "pw-anna")
	if body := off.do(lan, "GET", "/web/account", nil, oc).Body.String(); strings.Contains(body, "quickconnect") {
		t.Error("quick connect offered while off")
	}
}

func userID(t *testing.T, f *fixture, name string) string {
	t.Helper()
	u, err := f.db.UserByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// postFrom sends an empty form with the given Origin.
func (f *fixture) postFrom(from sender, path, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "http://example.com"+path, strings.NewReader(""))
	req.RemoteAddr = from.remote
	for k, v := range from.header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}
