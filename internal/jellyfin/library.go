package jellyfin

import (
	"net/http"

	"homeflix/internal/clientaddr"
)

// QueryResult is Jellyfin's paged list envelope.
type QueryResult[T any] struct {
	Items            []T `json:"Items"`
	TotalRecordCount int `json:"TotalRecordCount"`
	StartIndex       int `json:"StartIndex"`
}

func (s *Server) registerLibrary(rt *Router) {
	rt.HandleFunc(http.MethodPost, "/Library/Refresh", s.authed(s.refreshLibrary))
}

// refreshLibrary starts a scan of all libraries. Administrators only.
func (s *Server) refreshLibrary(w http.ResponseWriter, r *http.Request, sess *Session) {
	if !sess.User.IsAdmin {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !s.adminAllowed(r) {
		s.Log.Warn("library scan refused outside the admin networks", "user", sess.User.Name, "remote", clientaddr.From(r).String())
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if s.RequestScan != nil {
		s.RequestScan()
	}
	s.Log.Info("library scan requested", "user", sess.User.Name, "client", sess.Token.Client)
	w.WriteHeader(http.StatusNoContent)
}
