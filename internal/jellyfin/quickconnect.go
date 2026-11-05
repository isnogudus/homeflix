package jellyfin

import (
	"errors"
	"net/http"
	"time"

	"homeflix/internal/quickconnect"
	"homeflix/internal/store"
)

// QuickConnectResult is Jellyfin's view of a pending Quick Connect request.
type QuickConnectResult struct {
	Authenticated bool      `json:"Authenticated"`
	Secret        string    `json:"Secret"`
	Code          string    `json:"Code"`
	DeviceID      string    `json:"DeviceId"`
	DeviceName    string    `json:"DeviceName"`
	AppName       string    `json:"AppName"`
	AppVersion    string    `json:"AppVersion"`
	DateAdded     time.Time `json:"DateAdded"`
}

func quickConnectResult(q quickconnect.Request) QuickConnectResult {
	return QuickConnectResult{Authenticated: q.Authenticated(), Secret: q.Secret, Code: q.Code, DeviceID: q.DeviceID,
		DeviceName: q.DeviceName, AppName: q.AppName, AppVersion: q.AppVersion, DateAdded: q.Added.UTC()}
}

func (s *Server) registerQuickConnect(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/QuickConnect/Enabled", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.QuickConnect != nil)
	})
	rt.HandleFunc(http.MethodPost, "/QuickConnect/Initiate", s.quickConnect(s.quickConnectInitiate))
	rt.HandleFunc(http.MethodGet, "/QuickConnect/Connect", s.quickConnect(s.quickConnectStatus))
	rt.HandleFunc(http.MethodPost, "/QuickConnect/Authorize", s.quickConnect(s.authed(s.quickConnectAuthorize)))
	rt.HandleFunc(http.MethodPost, "/Users/AuthenticateWithQuickConnect", s.quickConnect(s.authenticateWithQuickConnect))
}

// quickConnect answers as Jellyfin does with Quick Connect off, so that
// clients tell the user instead of reporting an unknown error.
func (s *Server) quickConnect(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.QuickConnect == nil {
			http.Error(w, "Quick connect is disabled", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Server) quickConnectInitiate(w http.ResponseWriter, r *http.Request) {
	hdr := parseAuthHeader(r)
	if hdr.DeviceID == "" || hdr.Device == "" || hdr.Client == "" || hdr.Version == "" {
		http.Error(w, "authorization header must name Client, Device, DeviceId and Version", http.StatusBadRequest)
		return
	}
	q, err := s.QuickConnect.Initiate(quickconnect.Request{DeviceID: hdr.DeviceID, DeviceName: hdr.Device,
		AppName: hdr.Client, AppVersion: hdr.Version, Remote: remoteHost(r)})
	if errors.Is(err, quickconnect.ErrTooMany) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Too many pending quick connect requests.", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.Log.Info("quick connect requested", "client", q.AppName, "device", q.DeviceName, "remote", q.Remote)
	writeJSON(w, http.StatusOK, quickConnectResult(q))
}

func (s *Server) quickConnectStatus(w http.ResponseWriter, r *http.Request) {
	q, err := s.QuickConnect.Status(queryValue(r, "secret"))
	if err != nil {
		http.Error(w, "Unknown secret", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, quickConnectResult(q))
}

// quickConnectAuthorize confirms a code for the caller, or, for an
// administrator from the admin networks, for the user named by userId.
func (s *Server) quickConnectAuthorize(w http.ResponseWriter, r *http.Request, sess *Session) {
	user := sess.User
	if id := normalizeID(queryValue(r, "userId")); id != "" && id != user.ID {
		if !user.IsAdmin || !s.adminAllowed(r) {
			http.Error(w, "Access to the requested user is required.", http.StatusForbidden)
			return
		}
		u, err := s.Store.UserByID(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "Unknown user id.", http.StatusForbidden)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		user = u
	}
	q, err := s.QuickConnect.Authorize(queryValue(r, "code"), user.ID)
	switch {
	case errors.Is(err, quickconnect.ErrUnknown):
		http.Error(w, "Unable to find request", http.StatusNotFound)
		return
	case errors.Is(err, quickconnect.ErrAuthorized):
		http.Error(w, "Request is already authorized", http.StatusBadRequest)
		return
	case errors.Is(err, quickconnect.ErrTooMany):
		http.Error(w, "Too many wrong codes. Try again later.", http.StatusTooManyRequests)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.Log.Info("quick connect authorized", "user", user.Name, "by", sess.User.Name, "client", q.AppName,
		"device", q.DeviceName)
	writeJSON(w, http.StatusOK, true)
}

type quickConnectSecret struct {
	Secret string
}

func (s *Server) authenticateWithQuickConnect(w http.ResponseWriter, r *http.Request) {
	var req quickConnectSecret
	if !decodeJSON(w, r, &req) {
		return
	}
	q, err := s.QuickConnect.Redeem(req.Secret)
	if err != nil {
		http.Error(w, "Unknown or unconfirmed quick connect secret.", http.StatusUnauthorized)
		return
	}
	user, err := s.Store.UserByID(r.Context(), q.UserID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Unknown or unconfirmed quick connect secret.", http.StatusUnauthorized)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.signIn(w, r, user, store.Token{DeviceID: q.DeviceID, DeviceName: q.DeviceName, Client: q.AppName,
		ClientVersion: q.AppVersion, RemoteAddr: remoteHost(r)}, "quick connect")
}
