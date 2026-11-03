package jellyfin

import "net/http"

// QueryResult is Jellyfin's paged list envelope.
type QueryResult[T any] struct {
	Items            []T `json:"Items"`
	TotalRecordCount int `json:"TotalRecordCount"`
	StartIndex       int `json:"StartIndex"`
}

func (s *Server) registerLibrary(rt *Router) {
	// Until the library scanner exists these answer with empty results, so
	// clients show an empty home screen instead of errors.
	emptyPage := s.authed(s.emptyQueryResult)
	emptyList := s.authed(s.emptyList)
	for _, p := range []string{
		"/UserViews", "/Users/{userId}/Views",
		"/Items", "/Users/{userId}/Items",
		"/UserItems/Resume", "/Users/{userId}/Items/Resume",
		"/Shows/NextUp",
	} {
		rt.HandleFunc(http.MethodGet, p, emptyPage)
	}
	for _, p := range []string{"/Items/Latest", "/Users/{userId}/Items/Latest"} {
		rt.HandleFunc(http.MethodGet, p, emptyList)
	}
	rt.HandleFunc(http.MethodPost, "/Library/Refresh", s.authed(s.refreshLibrary))
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

func (s *Server) emptyQueryResult(w http.ResponseWriter, r *http.Request, sess *Session) {
	if _, ok := s.targetUser(w, r, sess); !ok {
		return
	}
	writeJSON(w, http.StatusOK, QueryResult[struct{}]{Items: []struct{}{}})
}

func (s *Server) emptyList(w http.ResponseWriter, r *http.Request, sess *Session) {
	if _, ok := s.targetUser(w, r, sess); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []struct{}{})
}
