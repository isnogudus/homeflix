package jellyfin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/auth"
	"homeflix/internal/clientaddr"
	"homeflix/internal/store"
)

// Session is the authenticated caller of a request.
type Session struct {
	User      store.User
	Token     store.Token
	tokenHash string
}

type authedHandler func(w http.ResponseWriter, r *http.Request, sess *Session)

// touchInterval limits how often request activity is written to the
// database for one token.
const touchInterval = time.Minute

// maxBodyBytes caps JSON request bodies.
const maxBodyBytes = 1 << 20

// requestToken finds the access token in the places Jellyfin clients put it.
func requestToken(r *http.Request) string {
	if t := parseAuthHeader(r).Token; t != "" {
		return t
	}
	for _, h := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if t := r.Header.Get(h); t != "" {
			return t
		}
	}
	if t := queryValue(r, "ApiKey"); t != "" {
		return t
	}
	return queryValue(r, "api_key")
}

// queryValue returns a query parameter, matching its name case-insensitively
// as Jellyfin does.
func queryValue(r *http.Request, name string) string {
	q := r.URL.Query()
	if v := q.Get(name); v != "" {
		return v
	}
	for k, vs := range q {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// remoteHost is the client address, behind trusted proxies.
func remoteHost(r *http.Request) string { return clientaddr.From(r).String() }

// authed wraps h so it only runs for requests with a valid access token.
func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := requestToken(r)
		if token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		hash := auth.HashToken(token)
		tok, user, err := s.Store.TokenByHash(r.Context(), hash)
		if errors.Is(err, store.ErrNotFound) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		now := time.Now()
		if now.Sub(tok.LastSeenAt) >= touchInterval {
			remote := remoteHost(r)
			if err := s.Store.TouchToken(r.Context(), hash, remote, now); err != nil {
				s.Log.Warn("record session activity", "err", err)
			} else {
				tok.LastSeenAt, tok.RemoteAddr = now, remote
				user.LastActivityAt = now
			}
		}
		h(w, r, &Session{User: user, Token: tok, tokenHash: hash})
	}
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// decodeJSON reads a JSON request body. Field names match case-insensitively.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

type authenticateByNameRequest struct {
	Username string
	Pw       string
}

// AuthenticationResult is the response of a successful login.
type AuthenticationResult struct {
	User        UserDto        `json:"User"`
	SessionInfo SessionInfoDto `json:"SessionInfo"`
	AccessToken string         `json:"AccessToken"`
	ServerID    string         `json:"ServerId"`
}

func (s *Server) authenticateByName(w http.ResponseWriter, r *http.Request) {
	var req authenticateByNameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	hdr := parseAuthHeader(r)
	if hdr.Client == "" || hdr.DeviceID == "" {
		http.Error(w, "authorization header must name Client and DeviceId", http.StatusBadRequest)
		return
	}
	if req.Username == "" {
		http.Error(w, "Username is required", http.StatusBadRequest)
		return
	}
	if s.Logins != nil {
		if wait, ok := s.Logins.Check(req.Username, remoteHost(r)); !ok {
			s.Log.Warn("login refused after too many failures", "user", req.Username, "client", hdr.Client,
				"remote", remoteHost(r), "retry_after", wait.Round(time.Second))
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			http.Error(w, "Too many failed logins. Try again later.", http.StatusTooManyRequests)
			return
		}
	}

	user, err := s.Store.UserByName(r.Context(), req.Username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.VerifyNoUser(req.Pw)
		s.loginFailed(w, r, req.Username, hdr)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	ok, err := auth.VerifyPassword(user.PasswordHash, req.Pw)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		s.loginFailed(w, r, req.Username, hdr)
		return
	}
	if s.Logins != nil {
		s.Logins.Succeeded(req.Username)
	}

	device := hdr.Device
	if device == "" {
		device = "Unknown"
	}
	s.signIn(w, r, user, store.Token{DeviceID: hdr.DeviceID, DeviceName: device, Client: hdr.Client,
		ClientVersion: hdr.Version, RemoteAddr: remoteHost(r)}, "password")
}

// signIn issues a token for user on the device described by dev and
// answers with the authentication result.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, user store.User, dev store.Token, method string) {
	token, err := auth.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sessionID, err := store.NewID()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := time.Now().UTC()
	tok := dev
	tok.Hash, tok.SessionID, tok.UserID, tok.CreatedAt, tok.LastSeenAt = auth.HashToken(token), sessionID, user.ID, now, now
	if err := s.Store.CreateToken(r.Context(), tok); err != nil {
		s.internalError(w, r, err)
		return
	}
	user.LastLoginAt, user.LastActivityAt = now, now
	s.Log.Info("login", "user", user.Name, "client", tok.Client, "version", tok.ClientVersion,
		"device", tok.DeviceName, "remote", tok.RemoteAddr, "method", method)
	writeJSON(w, http.StatusOK, AuthenticationResult{
		User:        s.userDto(user),
		SessionInfo: s.sessionDto(tok, user),
		AccessToken: token,
		ServerID:    s.ServerID,
	})
}

func (s *Server) loginFailed(w http.ResponseWriter, r *http.Request, name string, hdr AuthHeader) {
	s.Log.Warn("login failed", "user", name, "client", hdr.Client, "device", hdr.Device, "remote", remoteHost(r))
	if s.Logins != nil {
		s.Logins.Failed(name, remoteHost(r))
	}
	if s.FailedLoginDelay > 0 {
		select {
		case <-time.After(s.FailedLoginDelay):
		case <-r.Context().Done():
		}
	}
	http.Error(w, "Invalid user name or password.", http.StatusUnauthorized)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess *Session) {
	if err := s.Store.DeleteToken(r.Context(), sess.tokenHash); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	s.Log.Info("logout", "user", sess.User.Name, "client", sess.Token.Client, "device", sess.Token.DeviceName)
	w.WriteHeader(http.StatusNoContent)
}
