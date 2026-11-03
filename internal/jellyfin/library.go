package jellyfin

import "net/http"

// QueryResult is Jellyfin's paged list envelope.
type QueryResult[T any] struct {
	Items            []T `json:"Items"`
	TotalRecordCount int `json:"TotalRecordCount"`
	StartIndex       int `json:"StartIndex"`
}

func (s *Server) registerLibrary(rt *Router) {
	// Resume and next-up lists need playback state, which arrives in M4.
	for _, p := range []string{"/UserItems/Resume", "/Users/{userId}/Items/Resume", "/Shows/NextUp"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.emptyQueryResult))
	}
	rt.HandleFunc(http.MethodPost, "/Library/Refresh", s.authed(s.refreshLibrary))
}

func (s *Server) emptyQueryResult(w http.ResponseWriter, r *http.Request, sess *Session) {
	if _, ok := s.targetUser(w, r, sess); !ok {
		return
	}
	writeJSON(w, http.StatusOK, QueryResult[struct{}]{Items: []struct{}{}})
}

// refreshLibrary starts a scan of all libraries. Administrators only.
func (s *Server) refreshLibrary(w http.ResponseWriter, r *http.Request, sess *Session) {
	if !sess.User.IsAdmin {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if s.RequestScan != nil {
		s.RequestScan()
	}
	s.Log.Info("library scan requested", "user", sess.User.Name, "client", sess.Token.Client)
	w.WriteHeader(http.StatusNoContent)
}
