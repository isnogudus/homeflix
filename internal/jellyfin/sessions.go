package jellyfin

import (
	"io"
	"net/http"
)

func (s *Server) registerSessions(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/Sessions", s.authed(s.listSessions))
	rt.HandleFunc(http.MethodPost, "/Sessions/Capabilities", s.authed(s.reportCapabilities))
	rt.HandleFunc(http.MethodPost, "/Sessions/Capabilities/Full", s.authed(s.reportCapabilities))
}

// reportCapabilities accepts what a client announces about itself. homeflix
// offers no remote control, so nothing is kept.
func (s *Server) reportCapabilities(w http.ResponseWriter, r *http.Request, sess *Session) {
	io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxBodyBytes))
	w.WriteHeader(http.StatusNoContent)
}

// listSessions shows administrators every signed-in device and other users
// their own.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, sess *Session) {
	userID := sess.User.ID
	if sess.User.IsAdmin {
		userID = ""
	}
	tokens, err := s.Store.Tokens(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names := map[string]string{sess.User.ID: sess.User.Name}
	out := make([]SessionInfoDto, 0, len(tokens))
	for _, t := range tokens {
		u := sess.User
		if t.UserID != u.ID {
			if _, ok := names[t.UserID]; !ok {
				other, err := s.Store.UserByID(r.Context(), t.UserID)
				if err != nil {
					s.internalError(w, r, err)
					return
				}
				names[t.UserID] = other.Name
			}
			u.ID, u.Name = t.UserID, names[t.UserID]
		}
		out = append(out, s.sessionDto(t, u))
	}
	writeJSON(w, http.StatusOK, out)
}
