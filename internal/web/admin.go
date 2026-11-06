package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/auth"
	"homeflix/internal/catalog"
	"homeflix/internal/clientaddr"
	"homeflix/internal/library"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

// ScanStatus describes the library scans for the admin UI.
type ScanStatus struct {
	Running  bool
	Reason   string // startup, interval or requested
	Started  time.Time
	Finished time.Time
	Report   library.Report
	Err      string
}

// Setting is one configuration statement as the admin UI shows it.
type Setting struct {
	Name, Value string
}

func (s *Server) registerAdmin(mux *http.ServeMux) {
	admin := func(h sessionHandler) http.HandlerFunc { return s.signedIn(s.admin(h)) }
	mux.HandleFunc("GET /admin", redirect("/admin/"))
	mux.HandleFunc("GET /admin/{$}", admin(s.adminOverview))
	mux.HandleFunc("GET /admin/users", admin(s.adminUsers))
	mux.HandleFunc("POST /admin/users", admin(s.adminCreateUser))
	mux.HandleFunc("GET /admin/users/{id}", admin(s.adminUser))
	mux.HandleFunc("POST /admin/users/{id}/password", admin(s.adminSetPassword))
	mux.HandleFunc("POST /admin/users/{id}/admin", admin(s.adminSetAdmin))
	mux.HandleFunc("POST /admin/users/{id}/delete", admin(s.adminDeleteUser))
	mux.HandleFunc("POST /admin/users/{id}/sessions/{session}/end", admin(s.adminEndSession))
	mux.HandleFunc("POST /admin/users/{id}/passkeys/{cred}/delete", admin(s.adminDeletePasskey))
	mux.HandleFunc("GET /admin/library", admin(s.adminLibrary))
	mux.HandleFunc("POST /admin/library/scan", admin(s.adminScan))
	mux.HandleFunc("GET /admin/metadata", admin(s.adminMetadata))
	mux.HandleFunc("GET /admin/metadata/{id}", admin(s.adminMovie))
	mux.HandleFunc("POST /admin/metadata/{id}/match", admin(s.adminMatch))
	mux.HandleFunc("POST /admin/metadata/{id}/clear", admin(s.adminClear))
	mux.HandleFunc("GET /admin/settings", admin(s.adminSettings))
}

// back redirects to path after a form, with a message key for the page
// (ok=… or err=…).
func back(w http.ResponseWriter, r *http.Request, path, kind, key string) {
	http.Redirect(w, r, path+"?"+kind+"="+url.QueryEscape(key), http.StatusSeeOther)
}

// adminData starts the data of an admin page: the tab and the message
// from the last form, if it names a known text.
func adminData(r *http.Request, tab string) map[string]any {
	d := map[string]any{"Tab": tab}
	for _, kind := range []string{"ok", "err"} {
		if k := r.URL.Query().Get(kind); k != "" {
			if _, known := texts["en"][k]; known {
				d[strings.ToUpper(kind[:1])+kind[1:]] = k
			}
		}
	}
	return d
}

// metaCounts counts the movies by metadata state.
type metaCounts struct {
	Matched, NotFound, Failed, Pending int
}

func (s *Server) metaCounts(ctx context.Context) (metaCounts, []store.Item, map[string]store.Metadata, error) {
	var c metaCounts
	movies, err := s.Store.Movies(ctx)
	if err != nil {
		return c, nil, nil, err
	}
	all, err := s.Store.AllMetadata(ctx)
	if err != nil {
		return c, nil, nil, err
	}
	for _, it := range movies {
		switch md, ok := all[it.ID]; {
		case !ok || md.Status == store.MetaManual:
			c.Pending++
		case md.Status == store.MetaMatched:
			c.Matched++
		case md.Status == store.MetaNotFound:
			c.NotFound++
		default:
			c.Failed++
		}
	}
	return c, movies, all, nil
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request, sess *session) {
	ctx := r.Context()
	d := adminData(r, "overview")
	d["Version"], d["Started"], d["Client"] = s.Version, s.Started, clientaddr.From(r)
	if !s.Started.IsZero() {
		d["Uptime"] = time.Since(s.Started).Round(time.Minute)
	}
	users, err := s.Store.Users(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	toks, err := s.Store.Tokens(ctx, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	d["Users"], d["Sessions"] = len(users), len(toks)
	if s.Catalog != nil {
		snap := s.Catalog.Snapshot()
		people, withDetails := 0, 0
		for _, f := range snap.Facets("person", "", "") {
			people++
			if f.Person != nil {
				withDetails++
			}
		}
		d["Movies"], d["Collections"], d["People"], d["PeopleWithDetails"] =
			len(snap.AllMovies()), len(snap.Collections()), people, withDetails
	}
	mc, _, _, err := s.metaCounts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	d["Meta"], d["TMDB"] = mc, s.Metadata != nil
	if s.Outbound != nil {
		d["Outbound"] = s.Outbound.Counts()
	}
	if s.ScanStatus != nil {
		d["Scan"] = s.ScanStatus()
	}
	if s.HLS != nil {
		d["Remuxing"] = s.HLS.Sessions()
		if kf, err := s.Store.KeyframeStats(ctx); err == nil {
			d["Keyframes"] = kf
		}
	}
	s.render(w, r, http.StatusOK, "admin", sess, d)
}

// userRow is a user with its devices for the users pages.
type userRow struct {
	store.User
	Devices []store.Token
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request, sess *session) {
	ctx := r.Context()
	users, err := s.Store.Users(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	toks, err := s.Store.Tokens(ctx, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	byUser := map[string][]store.Token{}
	for _, t := range toks {
		byUser[t.UserID] = append(byUser[t.UserID], t)
	}
	var rows []userRow
	for _, u := range users {
		rows = append(rows, userRow{User: u, Devices: byUser[u.ID]})
	}
	d := adminData(r, "users")
	d["Users"] = rows
	s.render(w, r, http.StatusOK, "admin_users", sess, d)
}

// newPassword reads and checks the password fields of a form.
func newPassword(r *http.Request) (string, string) {
	pw := r.PostFormValue("password")
	switch {
	case pw == "":
		return "", "passwordEmpty"
	case pw != r.PostFormValue("confirm"):
		return "", "passwordMismatch"
	}
	return pw, ""
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request, sess *session) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		back(w, r, "/admin/users", "err", "nameEmpty")
		return
	}
	pw, problem := newPassword(r)
	if problem != "" {
		back(w, r, "/admin/users", "err", problem)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.fail(w, err)
		return
	}
	u, err := s.Store.CreateUser(r.Context(), name, hash, r.PostFormValue("admin") == "on")
	if errors.Is(err, store.ErrExists) {
		back(w, r, "/admin/users", "err", "userExists")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("user created", "user", u.Name, "admin", u.IsAdmin, "by", sess.User.Name)
	back(w, r, "/admin/users/"+u.ID, "ok", "userCreated")
}

// targetUser loads the user of the path, answering 404 if there is none.
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	u, err := s.Store.UserByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return u, false
	}
	if err != nil {
		s.fail(w, err)
		return u, false
	}
	return u, true
}

func (s *Server) adminUser(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	toks, err := s.Store.Tokens(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	keys, err := s.passkeyRows(r, u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	d := adminData(r, "users")
	d["User"], d["Self"] = userRow{User: u, Devices: toks}, u.ID == sess.User.ID
	d["Passkeys"] = keys
	d["OwnSession"] = sess.Token.SessionID
	s.render(w, r, http.StatusOK, "admin_user", sess, d)
}

func (s *Server) adminSetPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	pw, problem := newPassword(r)
	if problem != "" {
		back(w, r, "/admin/users/"+u.ID, "err", problem)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.fail(w, err)
		return
	}
	// This signs the user out everywhere, the admin too if it is their own.
	if err := s.Store.SetPassword(r.Context(), u.ID, hash); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("password changed", "user", u.Name, "by", sess.User.Name)
	if u.ID == sess.User.ID {
		http.Redirect(w, r, "/web/login?next="+url.QueryEscape("/admin/users/"+u.ID), http.StatusSeeOther)
		return
	}
	back(w, r, "/admin/users/"+u.ID, "ok", "passwordChanged")
}

func (s *Server) adminSetAdmin(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	admin := r.PostFormValue("admin") == "on"
	// Nobody removes their own rights: there would be no way back.
	if u.ID == sess.User.ID && !admin {
		back(w, r, "/admin/users/"+u.ID, "err", "ownAdmin")
		return
	}
	if err := s.Store.SetAdmin(r.Context(), u.ID, admin); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("admin rights changed", "user", u.Name, "admin", admin, "by", sess.User.Name)
	back(w, r, "/admin/users/"+u.ID, "ok", "rightsChanged")
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if u.ID == sess.User.ID {
		back(w, r, "/admin/users/"+u.ID, "err", "ownDelete")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.PostFormValue("confirm")), u.Name) {
		back(w, r, "/admin/users/"+u.ID, "err", "deleteConfirm")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), u.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("user deleted", "user", u.Name, "by", sess.User.Name)
	back(w, r, "/admin/users", "ok", "userDeleted")
}

func (s *Server) adminEndSession(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	err := s.Store.DeleteSession(r.Context(), u.ID, r.PathValue("session"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	s.Log.Info("session ended", "user", u.Name, "by", sess.User.Name)
	if r.PathValue("session") == sess.Token.SessionID {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	back(w, r, "/admin/users/"+u.ID, "ok", "sessionEnded")
}

// statRow is one line of a stream survey table.
type statRow struct {
	Name  string
	Count int
}

func statRows(c library.Counter) []statRow {
	var out []statRow
	for _, k := range c.Sorted() {
		out = append(out, statRow{k, c[k]})
	}
	return out
}

func (s *Server) adminLibrary(w http.ResponseWriter, r *http.Request, sess *session) {
	d := adminData(r, "library")
	if s.Catalog != nil {
		d["Libraries"] = s.Catalog.Snapshot().Libraries
	}
	if s.ScanStatus != nil {
		d["Scan"] = s.ScanStatus()
	}
	st, err := library.CollectStats(r.Context(), s.Store)
	if err != nil {
		s.fail(w, err)
		return
	}
	d["Stats"] = st
	d["Tables"] = []struct {
		Title string
		Rows  []statRow
	}{
		{"statContainers", statRows(st.Containers)}, {"statVideo", statRows(st.VideoCodecs)},
		{"statResolution", statRows(st.Resolutions)}, {"statHDR", statRows(st.HDR)},
		{"statAudio", statRows(st.AudioCodecs)}, {"statSubtitles", statRows(st.SubtitleCodecs)},
	}
	s.render(w, r, http.StatusOK, "admin_library", sess, d)
}

func (s *Server) adminScan(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.RequestScan == nil {
		back(w, r, "/admin/library", "err", "scanUnavailable")
		return
	}
	s.RequestScan()
	s.Log.Info("library scan requested", "user", sess.User.Name, "client", clientName)
	back(w, r, "/admin/library", "ok", "scanRequested")
}

// movieRow is one movie of the metadata list.
type movieRow struct {
	store.Item
	Meta      store.Metadata
	HasMeta   bool
	TMDBTitle string
}

func tmdbData(md store.Metadata) tmdb.Movie {
	var m tmdb.Movie
	if md.Data != "" {
		json.Unmarshal([]byte(md.Data), &m)
	}
	return m
}

// maxMovieRows bounds the "all movies" list without a search.
const maxMovieRows = 200

func (s *Server) adminMetadata(w http.ResponseWriter, r *http.Request, sess *session) {
	counts, movies, all, err := s.metaCounts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	showAll := r.URL.Query().Get("show") == "all" || q != ""
	key := catalog.SearchKey(q)
	var rows []movieRow
	more := 0
	for _, it := range movies {
		md, ok := all[it.ID]
		if !showAll && ok && md.Status == store.MetaMatched {
			continue
		}
		if key != "" && !strings.Contains(catalog.SearchKey(it.Title), key) &&
			!strings.Contains(catalog.SearchKey(tmdbData(md).Title), key) {
			continue
		}
		if showAll && q == "" && len(rows) >= maxMovieRows {
			more++
			continue
		}
		rows = append(rows, movieRow{Item: it, Meta: md, HasMeta: ok, TMDBTitle: tmdbData(md).Title})
	}
	d := adminData(r, "metadata")
	d["Movies"], d["Counts"], d["Query"], d["ShowAll"], d["More"], d["TMDB"] = rows, counts, q, showAll, more, s.Metadata != nil
	s.render(w, r, http.StatusOK, "admin_metadata", sess, d)
}

// targetMovie loads the movie of the path.
func (s *Server) targetMovie(w http.ResponseWriter, r *http.Request) (store.Item, bool) {
	movies, err := s.Store.Movies(r.Context())
	if err != nil {
		s.fail(w, err)
		return store.Item{}, false
	}
	for _, it := range movies {
		if it.ID == r.PathValue("id") {
			return it, true
		}
	}
	http.NotFound(w, r)
	return store.Item{}, false
}

func (s *Server) adminMovie(w http.ResponseWriter, r *http.Request, sess *session) {
	it, ok := s.targetMovie(w, r)
	if !ok {
		return
	}
	md, err := s.Store.GetMetadata(r.Context(), it.ID)
	hasMeta := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	d := adminData(r, "metadata")
	d["Movie"], d["Meta"], d["HasMeta"], d["TMDBMovie"], d["TMDB"] = it, md, hasMeta, tmdbData(md), s.Metadata != nil
	q, year := r.URL.Query().Get("q"), r.URL.Query().Get("year")
	if _, searched := r.URL.Query()["q"]; !searched {
		q = it.Title
		if it.Year > 0 {
			year = strconv.Itoa(it.Year)
		}
	}
	d["Query"], d["Year"] = q, year
	if _, searched := r.URL.Query()["q"]; searched && s.Metadata != nil && strings.TrimSpace(q) != "" {
		y, _ := strconv.Atoi(year)
		results, err := s.Metadata.TMDB.Search(r.Context(), strings.TrimSpace(q), y, s.Metadata.Language)
		if err != nil {
			s.Log.Warn("TMDB search failed", "query", q, "err", err)
			d["Err"] = "searchFailed"
		}
		sort.SliceStable(results, func(i, j int) bool { return results[i].Popularity > results[j].Popularity })
		d["Results"], d["Searched"] = results, true
	}
	s.render(w, r, http.StatusOK, "admin_movie", sess, d)
}

func (s *Server) adminMatch(w http.ResponseWriter, r *http.Request, sess *session) {
	it, ok := s.targetMovie(w, r)
	if !ok {
		return
	}
	path := "/admin/metadata/" + it.ID
	if s.Metadata == nil {
		back(w, r, path, "err", "tmdbOff")
		return
	}
	id, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("tmdb_id")))
	if err != nil || id <= 0 {
		back(w, r, path, "err", "tmdbID")
		return
	}
	if _, err := s.Metadata.Match(r.Context(), it, id); err != nil {
		s.Log.Warn("manual match failed", "item", it.Title, "tmdb_id", id, "err", err)
		back(w, r, path, "err", "matchFailed")
		return
	}
	s.Log.Info("manual match", "item", it.Title, "tmdb_id", id, "by", sess.User.Name)
	if s.Reload != nil {
		s.Reload()
	}
	back(w, r, path, "ok", "matchSaved")
}

func (s *Server) adminClear(w http.ResponseWriter, r *http.Request, sess *session) {
	it, ok := s.targetMovie(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteMetadata(r.Context(), it.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	s.Log.Info("match cleared", "item", it.Title, "by", sess.User.Name)
	if s.Reload != nil {
		s.Reload()
	}
	// The next metadata update, after the scan, looks the movie up again.
	if s.RequestScan != nil {
		s.RequestScan()
	}
	back(w, r, "/admin/metadata/"+it.ID, "ok", "cleared")
}

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	d := adminData(r, "settings")
	d["Settings"] = s.Settings
	s.render(w, r, http.StatusOK, "admin_settings", sess, d)
}

func (s *Server) adminDeletePasskey(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	s.deletePasskey(w, r, sess, u, "cred", "/admin/users/"+u.ID)
}
