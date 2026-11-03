package jellyfin

import (
	"encoding/json"
	"errors"
	"net/http"

	"homeflix/internal/store"
)

func (s *Server) registerDisplayPreferences(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/DisplayPreferences/{prefId}", s.authed(s.getDisplayPreferences))
	rt.HandleFunc(http.MethodPost, "/DisplayPreferences/{prefId}", s.authed(s.setDisplayPreferences))
}

func defaultDisplayPreferences(prefID, client string) map[string]any {
	return map[string]any{
		"Id":                 prefID,
		"ViewType":           "",
		"SortBy":             "SortName",
		"IndexBy":            "",
		"RememberIndexing":   false,
		"PrimaryImageHeight": 250,
		"PrimaryImageWidth":  250,
		"CustomPrefs":        map[string]any{},
		"ScrollDirection":    "Horizontal",
		"ShowBackdrop":       true,
		"RememberSorting":    false,
		"SortOrder":          "Ascending",
		"ShowSidebar":        false,
		"Client":             client,
	}
}

func prefClient(r *http.Request) string {
	if c := queryValue(r, "client"); c != "" {
		return c
	}
	return "emby"
}

func (s *Server) getDisplayPreferences(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	prefID, client := r.PathValue("prefId"), prefClient(r)
	prefs := defaultDisplayPreferences(prefID, client)
	stored, err := s.Store.DisplayPrefs(r.Context(), u.ID, prefID, client)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		s.internalError(w, r, err)
		return
	default:
		prefs = overlayJSON(prefs, stored)
	}
	prefs["Id"], prefs["Client"] = prefID, client
	writeJSON(w, http.StatusOK, prefs)
}

func (s *Server) setDisplayPreferences(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	var prefs map[string]json.RawMessage
	if !decodeJSON(w, r, &prefs) {
		return
	}
	if prefs == nil {
		http.Error(w, "display preferences must be a JSON object", http.StatusBadRequest)
		return
	}
	data, err := json.Marshal(prefs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.Store.SetDisplayPrefs(r.Context(), u.ID, r.PathValue("prefId"), prefClient(r), string(data)); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
