package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/auth"
	"homeflix/internal/clientaddr"
	"homeflix/internal/store"
)

// cookieName holds the session token. The token is a row of the token
// table, so web sessions show up and end like Jellyfin devices.
const cookieName = "homeflix_session"

// sessionLifetime is how long the browser keeps the cookie.
const sessionLifetime = 90 * 24 * time.Hour

// clientName names web sessions in the token table.
const clientName = "homeflix web"

type session struct {
	User  store.User
	Token store.Token
	raw   string // the token itself, from the cookie
}

// CSRF is the token forms carry; it is derived from the session token,
// which other sites cannot read.
func (sess *session) CSRF() string {
	if sess == nil {
		return ""
	}
	m := hmac.New(sha256.New, []byte(sess.raw))
	m.Write([]byte("csrf"))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess *session)

// current returns the session of the request's cookie, or nil.
func (s *Server) current(r *http.Request) (*session, error) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	hash := auth.HashToken(c.Value)
	tok, user, err := s.Store.TokenByHash(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if now := time.Now(); now.Sub(tok.LastSeenAt) >= time.Minute {
		if err := s.Store.TouchToken(r.Context(), hash, clientaddr.From(r).String(), now); err != nil {
			s.Log.Warn("record session activity", "err", err)
		}
	}
	return &session{User: user, Token: tok, raw: c.Value}, nil
}

// signedIn wraps h so it runs only with a session; others go to the login
// page and come back afterwards. Forms must carry the session's CSRF token.
func (s *Server) signedIn(h sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.current(r)
		if err != nil {
			s.Log.Error("read session", "err", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if sess == nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/web/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.CSRF())) != 1 {
				http.Error(w, "invalid form token; reload the page", http.StatusForbidden)
				return
			}
		}
		h(w, r, sess)
	}
}

// nextPage is where to go after login: a page of the web client or the
// admin UI, never another site.
func nextPage(v string) string {
	if (strings.HasPrefix(v, "/web/") || strings.HasPrefix(v, "/admin/")) && !strings.ContainsAny(v, "\\") &&
		!strings.HasPrefix(v, "//") {
		return v
	}
	return "/web/"
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if sess, _ := s.current(r); sess != nil {
		http.Redirect(w, r, nextPage(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	s.render(w, r, http.StatusOK, "login", nil, map[string]any{"Next": nextPage(r.URL.Query().Get("next")),
		"Passkeys": s.passkeyOrigin(r) != ""})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	name := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	next := nextPage(r.PostFormValue("next"))
	remote := clientaddr.From(r).String()
	again := func(status int, msg string) {
		s.render(w, r, status, "login", nil, map[string]any{"Next": next, "Username": name, "Error": msg,
			"Passkeys": s.passkeyOrigin(r) != ""})
	}
	if name == "" {
		again(http.StatusBadRequest, "loginMissing")
		return
	}
	if s.Logins != nil {
		if wait, ok := s.Logins.Check(name, remote); !ok {
			s.Log.Warn("login refused after too many failures", "user", name, "client", clientName, "remote", remote,
				"retry_after", wait.Round(time.Second))
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			again(http.StatusTooManyRequests, "loginLocked")
			return
		}
	}
	user, err := s.Store.UserByName(r.Context(), name)
	ok := false
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.VerifyNoUser(password)
	case err != nil:
		s.fail(w, err)
		return
	default:
		if ok, err = auth.VerifyPassword(user.PasswordHash, password); err != nil {
			s.fail(w, err)
			return
		}
	}
	if !ok {
		s.Log.Warn("login failed", "user", name, "client", clientName, "remote", remote)
		if s.Logins != nil {
			s.Logins.Failed(name, remote)
		}
		if s.FailedLoginDelay > 0 {
			select {
			case <-time.After(s.FailedLoginDelay):
			case <-r.Context().Done():
			}
		}
		again(http.StatusUnauthorized, "loginFailed")
		return
	}
	if s.Logins != nil {
		s.Logins.Succeeded(name)
	}
	if err := s.startSession(w, r, user, "password"); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// startSession signs user in on this browser: a token for a new device
// and the cookie that carries it.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user store.User, method string) error {
	raw, err := auth.NewToken()
	if err != nil {
		return err
	}
	sessionID, err := store.NewID()
	if err != nil {
		return err
	}
	deviceID, err := store.NewID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tok := store.Token{Hash: auth.HashToken(raw), SessionID: sessionID, UserID: user.ID, DeviceID: deviceID,
		DeviceName: deviceName(r.UserAgent()), Client: clientName, RemoteAddr: clientaddr.From(r).String(),
		CreatedAt: now, LastSeenAt: now}
	if err := s.Store.CreateToken(r.Context(), tok); err != nil {
		return err
	}
	s.Log.Info("login", "user", user.Name, "client", clientName, "device", tok.DeviceName, "remote", tok.RemoteAddr,
		"method", method)
	http.SetCookie(w, s.cookie(r, raw, sessionLifetime))
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess *session) {
	if err := s.Store.DeleteToken(r.Context(), sess.Token.Hash); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	s.Log.Info("logout", "user", sess.User.Name, "client", clientName, "device", sess.Token.DeviceName)
	http.SetCookie(w, s.cookie(r, "", -1))
	http.Redirect(w, r, "/web/login", http.StatusSeeOther)
}

// cookie makes the session cookie; maxAge < 0 deletes it. It is Secure
// when the client came over HTTPS, as through Caddy or tailscale serve.
func (s *Server) cookie(r *http.Request, value string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: clientaddr.From(r).Scheme == "https"}
	if maxAge < 0 {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(maxAge.Seconds())
	}
	return c
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.Log.Error("web request failed", "err", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// deviceName names a browser from its user agent, as "Firefox on Linux".
func deviceName(ua string) string {
	browser := "Browser"
	for _, b := range []struct{ token, name string }{
		{"Edg/", "Edge"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	for _, o := range []struct{ token, name string }{
		{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"}, {"Mac OS X", "macOS"},
		{"Windows", "Windows"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, o.token) {
			return browser + " on " + o.name
		}
	}
	return browser
}
