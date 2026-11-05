package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/clientaddr"
	"homeflix/internal/passkey"
	"homeflix/internal/quickconnect"
	"homeflix/internal/store"
)

// maxPasskeyName bounds the name a user gives a passkey.
const maxPasskeyName = 64

func (s *Server) registerPasskeys(mux *http.ServeMux) {
	mux.HandleFunc("POST /web/passkey/begin", s.passkeyLoginBegin)
	mux.HandleFunc("POST /web/passkey/finish", s.passkeyLoginFinish)
	mux.HandleFunc("POST /web/account/passkeys/begin", s.signedIn(s.passkeyAddBegin))
	mux.HandleFunc("POST /web/account/passkeys/finish", s.signedIn(s.passkeyAddFinish))
	mux.HandleFunc("POST /web/account/passkeys/{id}/delete", s.signedIn(s.passkeyDeleteOwn))
	mux.HandleFunc("POST /web/account/quickconnect", s.signedIn(s.quickConnectAuthorize))
}

// passkeyOrigin is the origin of the request if passkeys are offered on
// it, else "".
func (s *Server) passkeyOrigin(r *http.Request) string {
	if o := clientaddr.From(r).Origin(); s.Passkeys.Offered(o) {
		return o
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// jsonError answers a script with a text of the pages.
func (s *Server) jsonError(w http.ResponseWriter, status int, key string) {
	writeJSON(w, status, map[string]string{"error": s.text(key)})
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	origin := s.passkeyOrigin(r)
	if origin == "" {
		s.jsonError(w, http.StatusNotFound, "passkeyNotHere")
		return
	}
	opts, err := s.Passkeys.BeginLogin(origin, r.PostFormValue("conditional") == "1")
	if errors.Is(err, passkey.ErrBusy) {
		s.jsonError(w, http.StatusServiceUnavailable, "passkeyFailed")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	origin := s.passkeyOrigin(r)
	if origin == "" {
		s.jsonError(w, http.StatusNotFound, "passkeyNotHere")
		return
	}
	remote := clientaddr.From(r).String()
	if s.Logins != nil {
		if wait, blocked := s.Logins.ByAddr.Blocked(remote); blocked {
			s.Log.Warn("passkey login refused after too many failures", "remote", remote, "retry_after", wait.Round(time.Second))
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			s.jsonError(w, http.StatusTooManyRequests, "loginLocked")
			return
		}
	}
	user, err := s.Passkeys.FinishLogin(r.Context(), origin, r.PostFormValue("ceremony"), []byte(r.PostFormValue("credential")))
	switch {
	case errors.Is(err, passkey.ErrCeremony):
		s.jsonError(w, http.StatusBadRequest, "passkeyExpired")
		return
	case errors.Is(err, passkey.ErrRejected):
		s.Log.Warn("passkey login failed", "remote", remote, "origin", origin, "err", err)
		if s.Logins != nil {
			s.Logins.ByAddr.Fail(remote)
		}
		s.jsonError(w, http.StatusUnauthorized, "passkeyFailed")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	if s.Logins != nil {
		s.Logins.Succeeded(user.Name)
	}
	if err := s.startSession(w, r, user, "passkey"); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": nextPage(r.PostFormValue("next"))})
}

func (s *Server) passkeyAddBegin(w http.ResponseWriter, r *http.Request, sess *session) {
	origin := s.passkeyOrigin(r)
	if origin == "" {
		s.jsonError(w, http.StatusNotFound, "passkeyNotHere")
		return
	}
	opts, err := s.Passkeys.BeginRegistration(r.Context(), origin, sess.User)
	if errors.Is(err, passkey.ErrBusy) {
		s.jsonError(w, http.StatusServiceUnavailable, "passkeyFailed")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) passkeyAddFinish(w http.ResponseWriter, r *http.Request, sess *session) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	origin := s.passkeyOrigin(r)
	if origin == "" {
		s.jsonError(w, http.StatusNotFound, "passkeyNotHere")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		name = deviceName(r.UserAgent())
	}
	if len([]rune(name)) > maxPasskeyName {
		name = string([]rune(name)[:maxPasskeyName])
	}
	p, err := s.Passkeys.FinishRegistration(r.Context(), origin, r.PostFormValue("ceremony"), sess.User, name,
		[]byte(r.PostFormValue("credential")))
	switch {
	case errors.Is(err, passkey.ErrCeremony):
		s.jsonError(w, http.StatusBadRequest, "passkeyExpired")
		return
	case errors.Is(err, passkey.ErrRejected), errors.Is(err, store.ErrExists):
		s.Log.Warn("passkey not added", "user", sess.User.Name, "origin", origin, "err", err)
		s.jsonError(w, http.StatusBadRequest, "passkeyFailed")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.Log.Info("passkey added", "user", sess.User.Name, "name", p.Name, "rp", p.RPID)
	writeJSON(w, http.StatusOK, map[string]string{"next": "/web/account?ok=passkeyAdded"})
}

// passkeyID decodes a credential ID from a path parameter.
func passkeyID(r *http.Request, param string) ([]byte, bool) {
	id, err := base64.RawURLEncoding.DecodeString(r.PathValue(param))
	return id, err == nil && len(id) > 0
}

func (s *Server) passkeyDeleteOwn(w http.ResponseWriter, r *http.Request, sess *session) {
	s.deletePasskey(w, r, sess, sess.User, "id", "/web/account")
}

func (s *Server) deletePasskey(w http.ResponseWriter, r *http.Request, sess *session, user store.User, param, page string) {
	id, ok := passkeyID(r, param)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := s.Store.DeletePasskey(r.Context(), user.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		back(w, r, page, "err", "passkeyGone")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("passkey removed", "user", user.Name, "by", sess.User.Name)
	back(w, r, page, "ok", "passkeyRemoved")
}

// passkeyRow is a passkey as the account and admin pages show it.
type passkeyRow struct {
	store.Passkey
	ID   string // for paths
	Here bool   // registered for the address the page is open on
}

func (s *Server) passkeyRows(r *http.Request, userID string) ([]passkeyRow, error) {
	keys, err := s.Store.Passkeys(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	here := s.Passkeys.RPID(s.passkeyOrigin(r))
	rows := make([]passkeyRow, len(keys))
	for i, k := range keys {
		rows[i] = passkeyRow{Passkey: k, ID: base64.RawURLEncoding.EncodeToString(k.CredentialID), Here: k.RPID == here}
	}
	return rows, nil
}

// quickConnectAuthorize confirms the code an app shows.
func (s *Server) quickConnectAuthorize(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.QuickConnect == nil {
		http.NotFound(w, r)
		return
	}
	code := strings.ReplaceAll(r.PostFormValue("code"), " ", "")
	q, err := s.QuickConnect.Authorize(code, sess.User.ID)
	switch {
	case errors.Is(err, quickconnect.ErrUnknown):
		back(w, r, "/web/account", "err", "codeUnknown")
		return
	case errors.Is(err, quickconnect.ErrAuthorized):
		back(w, r, "/web/account", "err", "codeUsed")
		return
	case errors.Is(err, quickconnect.ErrTooMany):
		back(w, r, "/web/account", "err", "codeLocked")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.Log.Info("quick connect authorized", "user", sess.User.Name, "client", q.AppName, "device", q.DeviceName)
	back(w, r, "/web/account", "ok", "codeConfirmed")
}
