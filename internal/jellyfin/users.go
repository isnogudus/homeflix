package jellyfin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"homeflix/internal/store"
)

func (s *Server) registerUsers(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/Users/Public", s.publicUsers)
	rt.HandleFunc(http.MethodPost, "/Users/AuthenticateByName", s.authenticateByName)
	rt.HandleFunc(http.MethodPost, "/Sessions/Logout", s.authed(s.logout))
	rt.HandleFunc(http.MethodGet, "/Users", s.authed(s.listUsers))
	rt.HandleFunc(http.MethodGet, "/Users/Me", s.authed(s.currentUser))
	rt.HandleFunc(http.MethodGet, "/Users/{userId}", s.authed(s.getUser))
	rt.HandleFunc(http.MethodPost, "/Users/Configuration", s.authed(s.updateUserConfiguration))
	rt.HandleFunc(http.MethodPost, "/Users/{userId}/Configuration", s.authed(s.updateUserConfiguration))
	// homeflix has no user avatars; answering here keeps the unsupported
	// endpoint log free of them.
	rt.HandleFunc(http.MethodGet, "/Users/{userId}/Images/{type}", http.NotFound)
}

// publicUsers lists users for a login screen picker. homeflix does not
// advertise accounts to unauthenticated clients, so the list is always
// empty and clients ask for name and password.
func (s *Server) publicUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []struct{}{})
}

// targetUser resolves the user a request is about: the userId path or query
// parameter, or the caller. Only administrators may address other users.
// It writes the error response and returns false when access is denied.
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request, sess *Session) (store.User, bool) {
	id := r.PathValue("userId")
	if id == "" {
		id = queryValue(r, "userId")
	}
	id = normalizeID(id)
	if id == "" || id == sess.User.ID {
		return sess.User, true
	}
	if !sess.User.IsAdmin {
		w.WriteHeader(http.StatusForbidden)
		return store.User{}, false
	}
	u, err := s.Store.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return store.User{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return store.User{}, false
	}
	return u, true
}

// normalizeID accepts IDs with or without dashes, in any case, and returns
// the 32-digit lowercase form used internally.
func normalizeID(id string) string {
	return strings.ToLower(strings.ReplaceAll(id, "-", ""))
}

func (s *Server) currentUser(w http.ResponseWriter, r *http.Request, sess *Session) {
	writeJSON(w, http.StatusOK, s.userDto(sess.User))
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.userDto(u))
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, sess *Session) {
	if !sess.User.IsAdmin {
		writeJSON(w, http.StatusOK, []UserDto{s.userDto(sess.User)})
		return
	}
	users, err := s.Store.Users(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]UserDto, 0, len(users))
	for _, u := range users {
		out = append(out, s.userDto(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateUserConfiguration(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	var cfg map[string]json.RawMessage
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if cfg == nil {
		http.Error(w, "configuration must be a JSON object", http.StatusBadRequest)
		return
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.Store.SetUserConfiguration(r.Context(), u.ID, string(data)); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
