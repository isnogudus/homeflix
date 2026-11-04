package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homeflix/internal/auth"
	"homeflix/internal/store"
)

const moonfinHeader = `MediaBrowser Client="Moonfin", Device="iMac", DeviceId="dev-1", Version="3.0.0"`

type apiClient struct {
	t     *testing.T
	h     http.Handler
	token string
}

func (c *apiClient) do(method, path, body string, header http.Header) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.168.1.20:50000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	auth := moonfinHeader
	if c.token != "" {
		auth += `, Token="` + c.token + `"`
	}
	req.Header.Set("Authorization", auth)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func addUser(t *testing.T, s *Server, name, password string, admin bool) store.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Store.CreateUser(context.Background(), name, hash, admin)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func login(t *testing.T, c *apiClient, name, password string) AuthenticationResult {
	t.Helper()
	rec := c.do("POST", "/Users/AuthenticateByName", `{"Username":"`+name+`","Pw":"`+password+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: status %d %q", name, rec.Code, rec.Body.String())
	}
	res := decode[AuthenticationResult](t, rec)
	c.token = res.AccessToken
	return res
}

func TestLogin(t *testing.T) {
	s := newTestServer(t, nil)
	anna := addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}

	res := login(t, c, "Anna", "pw")
	if len(res.AccessToken) != 32 || res.ServerID != s.ServerID || res.User.ID != anna.ID || res.User.Name != "anna" {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.User.Configuration == nil || res.User.Configuration["SubtitleMode"] != "Default" {
		t.Errorf("configuration missing defaults: %v", res.User.Configuration)
	}
	if !res.User.Policy.EnableMediaPlayback || res.User.Policy.IsAdministrator {
		t.Errorf("unexpected policy: %+v", res.User.Policy)
	}
	if res.SessionInfo.Client != "Moonfin" || res.SessionInfo.DeviceID != "dev-1" || res.SessionInfo.ID == "" {
		t.Errorf("unexpected session: %+v", res.SessionInfo)
	}

	// The raw JSON must carry Configuration as an object, which Moonfin
	// casts without a null check.
	raw := c.do("POST", "/Users/AuthenticateByName", `{"Username":"anna","Pw":"pw"}`, nil).Body.String()
	if !strings.Contains(raw, `"Configuration":{`) {
		t.Errorf("Configuration is not an object in %s", raw)
	}
}

func TestLoginFailures(t *testing.T) {
	s := newTestServer(t, nil)
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	for name, tc := range map[string]struct {
		body, header string
		status       int
	}{
		"wrong password":    {`{"Username":"anna","Pw":"nope"}`, moonfinHeader, 401},
		"unknown user":      {`{"Username":"bert","Pw":"pw"}`, moonfinHeader, 401},
		"empty name":        {`{"Username":"","Pw":"pw"}`, moonfinHeader, 400},
		"no device id":      {`{"Username":"anna","Pw":"pw"}`, `MediaBrowser Client="Moonfin"`, 400},
		"malformed body":    {`{"Username":`, moonfinHeader, 400},
		"lowercase members": {`{"username":"anna","pw":"pw"}`, moonfinHeader, 200},
	} {
		t.Run(name, func(t *testing.T) {
			rec := c.do("POST", "/Users/AuthenticateByName", tc.body, http.Header{"Authorization": {tc.header}})
			if rec.Code != tc.status {
				t.Errorf("status %d, want %d (%q)", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestTokenSources(t *testing.T) {
	s := newTestServer(t, nil)
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	token := login(t, c, "anna", "pw").AccessToken
	c.token = ""

	if rec := c.do("GET", "/Users/Me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d", rec.Code)
	}
	if rec := c.do("GET", "/Users/Me", "", http.Header{"X-Emby-Token": {"0000"}}); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token: status %d", rec.Code)
	}
	for name, req := range map[string]struct {
		path   string
		header http.Header
	}{
		"auth header":     {"/Users/Me", http.Header{"Authorization": {moonfinHeader + `, Token="` + token + `"`}}},
		"x-emby-token":    {"/Users/Me", http.Header{"X-Emby-Token": {token}}},
		"mediabrowser":    {"/Users/Me", http.Header{"X-Mediabrowser-Token": {token}}},
		"ApiKey query":    {"/Users/Me?ApiKey=" + token, nil},
		"api_key query":   {"/Users/Me?api_key=" + token, nil},
		"apikey any case": {"/Users/Me?apikey=" + token, nil},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := c.do("GET", req.path, "", req.header); rec.Code != http.StatusOK {
				t.Errorf("status %d", rec.Code)
			}
		})
	}
}

func TestUserAccess(t *testing.T) {
	s := newTestServer(t, nil)
	admin := addUser(t, s, "admin", "a", true)
	anna := addUser(t, s, "anna", "b", false)
	h := s.Handler()
	asAnna := &apiClient{t: t, h: h}
	login(t, asAnna, "anna", "b")
	asAdmin := &apiClient{t: t, h: h}
	login(t, asAdmin, "admin", "a")

	if rec := asAnna.do("GET", "/Users/"+anna.ID, "", nil); rec.Code != 200 {
		t.Errorf("own user: %d", rec.Code)
	}
	if rec := asAnna.do("GET", "/Users/"+admin.ID, "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("other user as non-admin: %d", rec.Code)
	}
	dashed := anna.ID[:8] + "-" + anna.ID[8:12] + "-" + anna.ID[12:16] + "-" + anna.ID[16:20] + "-" + anna.ID[20:]
	if rec := asAdmin.do("GET", "/Users/"+strings.ToUpper(dashed), "", nil); rec.Code != 200 {
		t.Errorf("admin reading dashed upper-case ID: %d", rec.Code)
	}
	if rec := asAdmin.do("GET", "/Users/ffffffffffffffffffffffffffffffff", "", nil); rec.Code != 404 {
		t.Errorf("unknown user: %d", rec.Code)
	}
	if users := decode[[]UserDto](t, asAnna.do("GET", "/Users", "", nil)); len(users) != 1 {
		t.Errorf("non-admin sees %d users", len(users))
	}
	if users := decode[[]UserDto](t, asAdmin.do("GET", "/Users", "", nil)); len(users) != 2 {
		t.Errorf("admin sees %d users", len(users))
	}
	if sessions := decode[[]SessionInfoDto](t, asAdmin.do("GET", "/Sessions", "", nil)); len(sessions) != 2 {
		t.Errorf("admin sees %d sessions", len(sessions))
	}
	if sessions := decode[[]SessionInfoDto](t, asAnna.do("GET", "/Sessions", "", nil)); len(sessions) != 1 || sessions[0].UserName != "anna" {
		t.Errorf("anna sees sessions %+v", sessions)
	}
}

func TestUserConfiguration(t *testing.T) {
	s := newTestServer(t, nil)
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")

	rec := c.do("POST", "/Users/Configuration", `{"SubtitleMode":"Smart","AudioLanguagePreference":"ger","Custom":1}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update: %d %q", rec.Code, rec.Body.String())
	}
	me := decode[UserDto](t, c.do("GET", "/Users/Me", "", nil))
	if me.Configuration["SubtitleMode"] != "Smart" || me.Configuration["AudioLanguagePreference"] != "ger" ||
		me.Configuration["PlayDefaultAudioTrack"] != true || me.Configuration["Custom"] != float64(1) {
		t.Errorf("configuration after update: %v", me.Configuration)
	}
	if rec := c.do("POST", "/Users/Configuration", `[1]`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("non-object configuration: %d", rec.Code)
	}
}

func TestDisplayPreferences(t *testing.T) {
	s := newTestServer(t, nil)
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")

	got := decode[map[string]any](t, c.do("GET", "/DisplayPreferences/usersettings?client=moonfin", "", nil))
	if got["Id"] != "usersettings" || got["Client"] != "moonfin" || got["SortBy"] != "SortName" {
		t.Errorf("defaults: %v", got)
	}
	rec := c.do("POST", "/DisplayPreferences/usersettings?client=moonfin",
		`{"Id":"usersettings","SortBy":"ProductionYear","CustomPrefs":{"homesection0":"resume"}}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save: %d", rec.Code)
	}
	got = decode[map[string]any](t, c.do("GET", "/DisplayPreferences/usersettings?client=moonfin", "", nil))
	if got["SortBy"] != "ProductionYear" || got["CustomPrefs"].(map[string]any)["homesection0"] != "resume" || got["ShowBackdrop"] != true {
		t.Errorf("after save: %v", got)
	}
	other := decode[map[string]any](t, c.do("GET", "/DisplayPreferences/usersettings?client=web", "", nil))
	if other["SortBy"] != "SortName" {
		t.Errorf("preferences leaked across clients: %v", other)
	}
}

func TestLogoutAndRelogin(t *testing.T) {
	s := newTestServer(t, nil)
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}

	first := login(t, c, "anna", "pw").AccessToken
	second := login(t, c, "anna", "pw").AccessToken
	if first == second {
		t.Fatal("relogin returned the same token")
	}
	c.token = first
	if rec := c.do("GET", "/Users/Me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("token replaced by relogin from same device still works: %d", rec.Code)
	}
	c.token = second
	if rec := c.do("POST", "/Sessions/Logout", "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	if rec := c.do("GET", "/Users/Me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("token works after logout: %d", rec.Code)
	}
}

func TestLibraryRefresh(t *testing.T) {
	s := newTestServer(t, nil)
	requested := 0
	s.RequestScan = func() { requested++ }
	addUser(t, s, "admin", "a", true)
	addUser(t, s, "anna", "b", false)
	h := s.Handler()
	asAnna := &apiClient{t: t, h: h}
	login(t, asAnna, "anna", "b")
	asAdmin := &apiClient{t: t, h: h}
	login(t, asAdmin, "admin", "a")

	if rec := asAnna.do("POST", "/Library/Refresh", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin refresh: %d", rec.Code)
	}
	if rec := asAdmin.do("POST", "/Library/Refresh", "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("admin refresh: %d", rec.Code)
	}
	if requested != 1 {
		t.Errorf("scan requested %d times, want 1", requested)
	}
}

func TestHomeScreenIsEmpty(t *testing.T) {
	s := newTestServer(t, nil)
	anna := addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")

	for _, p := range []string{"/UserViews", "/UserItems/Resume?userId=" + anna.ID, "/Users/" + anna.ID + "/Items", "/Shows/NextUp", "/LiveTv/Channels"} {
		rec := c.do("GET", p, "", nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Items":[]`) || !strings.Contains(rec.Body.String(), `"TotalRecordCount":0`) {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	if rec := c.do("GET", "/Items/Latest", "", nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("latest: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.do("POST", "/Sessions/Capabilities/Full", `{"PlayableMediaTypes":["Video"]}`, nil); rec.Code != http.StatusNoContent {
		t.Errorf("capabilities: %d", rec.Code)
	}
	info := decode[map[string]any](t, c.do("GET", "/System/Info", "", nil))
	if info["Id"] != s.ServerID || info["ProgramDataPath"] != "" {
		t.Errorf("system info: %v", info)
	}
}
