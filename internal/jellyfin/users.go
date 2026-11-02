package jellyfin

import "net/http"

func (s *Server) registerUsers(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/Users/Public", s.publicUsers)
}

// publicUsers lists users for a login screen picker. homeflix does not
// advertise accounts to unauthenticated clients, so the list is always
// empty and clients ask for name and password.
func (s *Server) publicUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []struct{}{})
}
