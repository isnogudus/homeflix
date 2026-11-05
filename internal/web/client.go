package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/auth"
	"homeflix/internal/catalog"
	"homeflix/internal/images"
	"homeflix/internal/playstate"
	"homeflix/internal/store"
)

func (s *Server) registerClient(mux *http.ServeMux) {
	mux.HandleFunc("GET /web/{$}", s.signedIn(s.home))
	mux.HandleFunc("GET /web/movies", s.signedIn(s.movies))
	mux.HandleFunc("GET /web/movies/{id}", s.signedIn(s.movie))
	mux.HandleFunc("POST /web/movies/{id}/played", s.signedIn(s.setPlayed))
	mux.HandleFunc("POST /web/movies/{id}/favorite", s.signedIn(s.setFavorite))
	mux.HandleFunc("GET /web/collections", s.signedIn(s.collections))
	mux.HandleFunc("GET /web/collections/{id}", s.signedIn(s.collection))
	mux.HandleFunc("GET /web/people/{id}", s.signedIn(s.person))
	mux.HandleFunc("GET /web/search", s.signedIn(s.search))
	mux.HandleFunc("GET /web/play/{id}", s.signedIn(s.play))
	mux.HandleFunc("GET /web/stream/{file}", s.signedIn(s.stream))
	mux.HandleFunc("GET /web/subtitles/{file}/{index}", s.signedIn(s.subtitleTrack))
	mux.HandleFunc("POST /web/progress", s.signedIn(s.progress))
	mux.HandleFunc("GET /web/hls/{file}/{audio}/index.m3u8", s.signedIn(s.hlsPlaylist))
	mux.HandleFunc("GET /web/hls/{file}/{audio}/{segment}", s.signedIn(s.hlsSegment))
	mux.HandleFunc("GET /web/account", s.signedIn(s.account))
	mux.HandleFunc("POST /web/account/password", s.signedIn(s.changeOwnPassword))
	mux.HandleFunc("POST /web/account/sessions/{session}/end", s.signedIn(s.endOwnSession))
}

// snapshot returns the current catalog, or an empty one.
func (s *Server) snapshot() *catalog.Snapshot {
	if s.Catalog == nil {
		return (&catalog.Catalog{}).Snapshot()
	}
	return s.Catalog.Snapshot()
}

// userState loads the user's play state of every item.
func (s *Server) userState(w http.ResponseWriter, r *http.Request, sess *session) (map[string]store.UserData, bool) {
	user, err := s.Store.AllUserData(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	return user, true
}

// clientData starts the data of a web client page.
func (s *Server) clientData(r *http.Request, sess *session, nav string) map[string]any {
	return map[string]any{"Nav": nav, "AdminLink": sess.User.IsAdmin && s.adminAllowed(r), "Query": r.URL.Query().Get("q")}
}

// card is a poster of a grid or a row.
type card struct {
	URL, Image, Title, Sub string
	Progress               int // percent watched, 0 if not started
	Played                 bool
}

func imageURL(id, kind, tag string, width, height int) string {
	if tag == "" {
		return ""
	}
	v := url.Values{"quality": {"85"}, "tag": {tag}}
	if height > 0 {
		v.Set("fillWidth", strconv.Itoa(width))
		v.Set("fillHeight", strconv.Itoa(height))
	} else {
		v.Set("maxWidth", strconv.Itoa(width))
	}
	return "/Items/" + id + "/Images/" + kind + "?" + v.Encode()
}

func posterURL(m *catalog.Movie) string {
	if m.Set != nil {
		return imageURL(m.ID, "Primary", images.CollectionTag(m.Set, images.Primary), 300, 450)
	}
	return imageURL(m.ID, "Primary", images.Tag(m, images.Primary), 300, 450)
}

// movieCard describes a movie, or the entry of a collection, as a poster.
func movieCard(m *catalog.Movie, user map[string]store.UserData) card {
	if m.Set != nil {
		return collectionCard(m.Set, user)
	}
	c := card{URL: "/web/movies/" + m.ID, Image: posterURL(m), Title: m.Name()}
	if m.Year > 0 {
		c.Sub = strconv.Itoa(m.Year)
	}
	d := user[m.ID]
	c.Played = d.Played
	if rt := playstate.Runtime(m); d.Position > 0 && rt > 0 {
		c.Progress = int(math.Ceil(100 * float64(d.Position) / float64(rt)))
	}
	return c
}

func collectionCard(c *catalog.Collection, user map[string]store.UserData) card {
	played := 0
	for _, m := range c.Movies {
		if user[m.ID].Played {
			played++
		}
	}
	return card{URL: "/web/collections/" + c.ID, Image: imageURL(c.ID, "Primary", images.CollectionTag(c, images.Primary), 300, 450),
		Title: c.Name, Sub: strconv.Itoa(len(c.Movies)), Played: played == len(c.Movies)}
}

func movieCards(ms []*catalog.Movie, user map[string]store.UserData) []card {
	out := make([]card, 0, len(ms))
	for _, m := range ms {
		out = append(out, movieCard(m, user))
	}
	return out
}

// resumeCard leads straight back into the player.
func resumeCard(r catalog.Resumable) card {
	m := r.Movie
	c := card{Title: m.Name(), Image: imageURL(m.ID, "Backdrop", images.Tag(m, images.Backdrop), 480, 270)}
	if len(r.Version.Parts) > 1 {
		c.Sub = fmt.Sprintf("%d/%d", r.Index+1, len(r.Version.Parts))
	}
	if rt := r.Part().Info.Duration; rt > 0 {
		c.Progress = int(math.Ceil(100 * float64(r.Data.Position) / float64(rt)))
	}
	c.URL = playURL(m, r.Version, r.Index, r.Data.Position)
	return c
}

func playURL(m *catalog.Movie, v *catalog.Version, part int, at time.Duration) string {
	q := url.Values{"version": {v.ID}}
	if part > 0 {
		q.Set("part", strconv.Itoa(part))
	}
	if at > 0 {
		q.Set("t", strconv.Itoa(int(at.Seconds())))
	}
	return "/web/play/" + m.ID + "?" + q.Encode()
}

func (s *Server) home(w http.ResponseWriter, r *http.Request, sess *session) {
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	snap := s.snapshot()
	var resume []card
	for _, rr := range snap.Resume(user, "", 12) {
		resume = append(resume, resumeCard(rr))
	}
	var cols []card
	for i, c := range snap.Collections() {
		if i == 18 {
			break
		}
		cols = append(cols, collectionCard(c, user))
	}
	favs, _ := snap.Movies(catalog.Query{Favorite: ptr(true), User: user, SortBy: []string{"SortName"}, Limit: 18})
	d := s.clientData(r, sess, "home")
	d["Resume"], d["Latest"], d["Collections"], d["Favorites"] = resume, movieCards(snap.Latest("", 18), user), cols, movieCards(favs, user)
	s.render(w, r, http.StatusOK, "home", sess, d)
}

func ptr[T any](v T) *T { return &v }

// pageSize is the number of posters per page of the grid.
const pageSize = 60

// link is a labelled link; Current marks the selected one of a group.
type link struct {
	Label, URL string
	Current    bool
}

// sorts are the orders of the grid: name, and the others newest or best first.
var sorts = []struct{ key, by string }{
	{"sortName", "SortName"}, {"sortAdded", "DateCreated"}, {"sortYear", "ProductionYear"}, {"sortRating", "CommunityRating"},
}

func (s *Server) movies(w http.ResponseWriter, r *http.Request, sess *session) {
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	q := r.URL.Query()
	with := func(k, v string) string {
		n := url.Values{}
		for key, vals := range q {
			n[key] = vals
		}
		if v == "" {
			n.Del(k)
		} else {
			n.Set(k, v)
		}
		if k != "page" {
			n.Del("page")
		}
		if len(n) == 0 {
			return "/web/movies"
		}
		return "/web/movies?" + n.Encode()
	}
	query := catalog.Query{User: user, Limit: pageSize}
	sortKey := q.Get("sort")
	var sortLinks []link
	for _, so := range sorts {
		cur := so.key == sortKey || (sortKey == "" && so.key == "sortName")
		if cur {
			query.SortBy = []string{so.by}
			query.Descending = []bool{so.key != "sortName"}
		}
		v := so.key
		if so.key == "sortName" {
			v = ""
		}
		sortLinks = append(sortLinks, link{Label: so.key, URL: with("sort", v), Current: cur})
	}
	letter := q.Get("letter")
	var letters []link
	for _, l := range strings.Split("#ABCDEFGHIJKLMNOPQRSTUVWXYZ", "") {
		letters = append(letters, link{Label: l, URL: with("letter", l), Current: l == letter})
	}
	switch {
	case letter == "#":
		query.NameLessThan = "a"
	case len(letter) == 1:
		query.NameStartsWith = strings.ToLower(letter)
	}
	snap := s.snapshot()
	genre := q.Get("genre")
	var genreName string
	if g, ok := snap.Genre(genre); ok {
		query.GenreIDs, genreName = []string{genre}, g.Name
	}
	show := q.Get("show")
	switch show {
	case "unplayed":
		query.Played = ptr(false)
	case "favorites":
		query.Favorite = ptr(true)
	}
	if q.Get("group") == "on" {
		query.Kinds, query.Collapse = catalog.MoviesAndCollections, true
	}
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	query.StartIndex = (page - 1) * pageSize
	ms, total := snap.Movies(query)
	d := s.clientData(r, sess, "movies")
	d["Cards"], d["Total"], d["Sorts"], d["Letters"], d["Genre"] = movieCards(ms, user), total, sortLinks, letters, genreName
	d["AllLetters"] = link{Label: "all", URL: with("letter", ""), Current: letter == ""}
	var shows []link
	for _, v := range []string{"", "unplayed", "favorites"} {
		label := "showAll"
		if v != "" {
			label = "show_" + v
		}
		shows = append(shows, link{Label: label, URL: with("show", v), Current: show == v})
	}
	d["Shows"] = shows
	group := link{Label: "groupCollections", URL: with("group", "on")}
	if q.Get("group") == "on" {
		group = link{Label: "groupCollections", URL: with("group", ""), Current: true}
	}
	d["Group"] = group
	if genreName != "" {
		d["ClearGenre"] = with("genre", "")
	}
	if page > 1 {
		d["Prev"] = with("page", strconv.Itoa(page-1))
	}
	if page*pageSize < total {
		d["Next"] = with("page", strconv.Itoa(page+1))
	}
	d["Page"], d["Pages"] = page, (total+pageSize-1)/pageSize
	s.render(w, r, http.StatusOK, "movies", sess, d)
}

// personCard is a member of cast or crew with portrait.
type personCard struct {
	URL, Image, Name, Role string
}

// versionView is a version a movie can be played in.
type versionView struct {
	Label, URL string
	Parts      int
	Current    bool
}

func durationText(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h > 0 {
		return fmt.Sprintf("%d h %d min", h, m)
	}
	return fmt.Sprintf("%d min", m)
}

// clock writes a position as 1:02:03.
func clock(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func (s *Server) movie(w http.ResponseWriter, r *http.Request, sess *session) {
	snap := s.snapshot()
	m, ok := snap.Item(r.PathValue("id"))
	if !ok || len(m.Versions) == 0 {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	version := m.Versions[0]
	for _, v := range m.Versions {
		if v.ID == r.URL.Query().Get("version") {
			version = v
		}
	}
	d := s.clientData(r, sess, "movies")
	d["Movie"], d["Name"] = m, m.Name()
	d["Poster"] = posterURL(m)
	if m.IsExtra() {
		d["Parent"] = link{Label: m.Parent.Name(), URL: "/web/movies/" + m.Parent.ID}
	} else {
		d["Backdrop"] = imageURL(m.ID, "Backdrop", images.Tag(m, images.Backdrop), 1600, 0)
	}
	d["Runtime"] = durationText(version.Duration)
	if meta := m.Meta; meta != nil {
		d["Overview"], d["Tagline"], d["Certification"] = meta.Overview, meta.Tagline, meta.Certification
		if meta.OriginalTitle != m.Name() {
			d["Original"] = meta.OriginalTitle
		}
		if meta.Rating > 0 {
			d["Rating"] = strconv.FormatFloat(meta.Rating, 'f', 1, 64)
		}
		var genres []link
		for _, g := range meta.Genres {
			genres = append(genres, link{Label: g, URL: "/web/movies?genre=" + catalog.GenreID(g)})
		}
		var crew, cast []personCard
		crewAt := map[string]int{} // a director who also wrote is listed once
		for _, p := range meta.People {
			pc := personCard{URL: "/web/people/" + catalog.PersonID(p.ID), Name: p.Name, Role: p.Role}
			if f, ok := snap.Person(catalog.PersonID(p.ID)); ok {
				pc.Image = imageURL(f.ID, "Primary", images.PhotoTag(f), 160, 240)
			}
			if p.Type == "Actor" {
				cast = append(cast, pc)
				continue
			}
			role := s.text(p.Type)
			if i, seen := crewAt[pc.URL]; seen {
				if !strings.Contains(crew[i].Role, role) {
					crew[i].Role += ", " + role
				}
				continue
			}
			pc.Role = role
			crewAt[pc.URL] = len(crew)
			crew = append(crew, pc)
		}
		d["Genres"], d["Crew"], d["Cast"] = genres, crew, cast
	}
	if c := m.Collection; c != nil {
		d["Collection"] = link{Label: c.Name, URL: "/web/collections/" + c.ID}
	}
	var versions []versionView
	for _, v := range m.Versions {
		versions = append(versions, versionView{Label: v.Label, URL: "/web/movies/" + m.ID + "?version=" + url.QueryEscape(v.ID),
			Parts: len(v.Parts), Current: v == version})
	}
	if len(versions) > 1 {
		d["Versions"] = versions
	}
	if len(version.Parts) > 1 {
		d["Parts"] = len(version.Parts)
	}
	info := version.Parts[0].Info
	var audio, subs []string
	for _, st := range info.Streams {
		switch st.Type {
		case "Audio":
			audio = append(audio, st.DisplayTitle)
		case "Subtitle":
			subs = append(subs, st.DisplayTitle)
		}
	}
	d["Audio"], d["Subtitles"] = audio, subs
	if a := defaultAudio(info); a != nil {
		d["PlaysAudio"] = a.DisplayTitle
	}
	mode, play := s.playable(version.Parts[0])
	d["Play"], d["Playable"], d["Remux"] = play, mode != "", mode == "hls"
	ud := user[m.ID]
	d["Played"], d["Favorite"] = ud.Played, ud.IsFavorite
	if mode != "" {
		d["PlayURL"] = playURL(m, version, 0, 0)
		// A later part may be where the user stopped.
		for i := len(version.Parts) - 1; i >= 0; i-- {
			id := m.ID
			if i > 0 {
				id = version.Parts[i].File.ID
			}
			if pos := user[id].Position; pos > 0 {
				d["ResumeURL"], d["ResumeAt"] = playURL(m, version, i, pos), clock(pos)
				if len(version.Parts) > 1 {
					d["ResumePart"] = i + 1
				}
				break
			}
		}
	}
	var extras []card
	for _, x := range m.Extras {
		c := movieCard(x, user)
		c.Sub = x.ExtraType
		extras = append(extras, c)
	}
	d["Extras"] = extras
	if !m.IsExtra() {
		d["Similar"] = movieCards(snap.Similar(m, 12), user)
	}
	s.render(w, r, http.StatusOK, "movie", sess, d)
}

func (s *Server) setPlayed(w http.ResponseWriter, r *http.Request, sess *session) {
	it, ok := playstate.Resolve(s.snapshot(), r.PathValue("id"), "")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := playstate.SetPlayed(r.Context(), s.Store, sess.User.ID, it, r.PostFormValue("played") == "on", time.Now().UTC()); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/web/movies/"+it.ID, http.StatusSeeOther)
}

func (s *Server) setFavorite(w http.ResponseWriter, r *http.Request, sess *session) {
	it, ok := playstate.Resolve(s.snapshot(), r.PathValue("id"), "")
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, err := s.Store.GetUserData(r.Context(), sess.User.ID, it.ID)
	if err == nil {
		d.IsFavorite = r.PostFormValue("favorite") == "on"
		err = s.Store.SaveUserData(r.Context(), sess.User.ID, d)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/web/movies/"+it.ID, http.StatusSeeOther)
}

func (s *Server) collections(w http.ResponseWriter, r *http.Request, sess *session) {
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	var cards []card
	for _, c := range s.snapshot().Collections() {
		cards = append(cards, collectionCard(c, user))
	}
	d := s.clientData(r, sess, "collections")
	d["Cards"] = cards
	s.render(w, r, http.StatusOK, "collections", sess, d)
}

func (s *Server) collection(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.snapshot().Collection(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	d := s.clientData(r, sess, "collections")
	d["Name"], d["Overview"], d["Cards"] = c.Name, c.Overview, movieCards(c.Movies, user)
	d["Poster"] = imageURL(c.ID, "Primary", images.CollectionTag(c, images.Primary), 300, 450)
	d["Backdrop"] = imageURL(c.ID, "Backdrop", images.CollectionTag(c, images.Backdrop), 1600, 0)
	s.render(w, r, http.StatusOK, "collection", sess, d)
}

func (s *Server) person(w http.ResponseWriter, r *http.Request, sess *session) {
	snap := s.snapshot()
	f, ok := snap.Person(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	ms, _ := snap.Movies(catalog.Query{PersonIDs: []string{f.ID}, User: user, SortBy: []string{"PremiereDate"}, Descending: []bool{true}})
	d := s.clientData(r, sess, "")
	d["Name"], d["Cards"] = f.Name, movieCards(ms, user)
	d["Photo"] = imageURL(f.ID, "Primary", images.PhotoTag(f), 300, 450)
	if p := f.Person; p != nil {
		d["Biography"], d["Place"] = p.Biography, p.PlaceOfBirth
		if b := p.Born(); !b.IsZero() {
			d["Born"] = b
		}
		if dd := p.Died(); !dd.IsZero() {
			d["Died"] = dd
		}
	}
	s.render(w, r, http.StatusOK, "person", sess, d)
}

// maxSearchResults bounds each kind of search result.
const maxSearchResults = 40

func (s *Server) search(w http.ResponseWriter, r *http.Request, sess *session) {
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d := s.clientData(r, sess, "")
	if q != "" {
		snap := s.snapshot()
		ms, total := snap.Movies(catalog.Query{Search: q, User: user, SortBy: []string{"SortName"}, Limit: maxSearchResults})
		var people []personCard
		for _, f := range snap.Facets("person", "", q) {
			if len(people) == maxSearchResults {
				break
			}
			people = append(people, personCard{URL: "/web/people/" + f.ID, Name: f.Name,
				Image: imageURL(f.ID, "Primary", images.PhotoTag(f), 160, 240)})
		}
		cols, _ := snap.Movies(catalog.Query{Kinds: catalog.CollectionsOnly, Search: q, User: user, Limit: maxSearchResults})
		d["Movies"], d["MovieTotal"], d["People"], d["Collections"] = movieCards(ms, user), total, people, movieCards(cols, user)
		d["Searched"] = true
	}
	s.render(w, r, http.StatusOK, "search", sess, d)
}

// playerPart is one file of the version the player plays.
type playerPart struct {
	Item     string        `json:"item"`   // the ID play state is kept under
	Source   string        `json:"source"` // the version
	Direct   string        `json:"direct"` // the file itself, "" if browsers cannot play it
	HLS      string        `json:"hls"`    // remux playlists below it: <hls><audio>/index.m3u8; "" if none
	Duration float64       `json:"duration"`
	Audio    []playerAudio `json:"audio"`
	Tracks   []playerTrack `json:"tracks"`
}

// playerAudio is an audio track the player offers.
type playerAudio struct {
	Index   int    `json:"index"`
	Label   string `json:"label"`
	Default bool   `json:"default"` // what the file plays without a choice
}

type playerTrack struct {
	Label   string `json:"label"`
	Lang    string `json:"lang"`
	Src     string `json:"src"`
	Default bool   `json:"default"`
}

func (s *Server) play(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.snapshot().Item(r.PathValue("id"))
	if !ok || len(m.Versions) == 0 {
		http.NotFound(w, r)
		return
	}
	version := m.Versions[0]
	for _, v := range m.Versions {
		if v.ID == r.URL.Query().Get("version") {
			version = v
		}
	}
	d := s.clientData(r, sess, "")
	d["Name"], d["Back"] = m.Name(), "/web/movies/"+m.ID
	if mode, why := s.playable(version.Parts[0]); mode == "" {
		d["Play"] = why
		s.render(w, r, http.StatusOK, "play", sess, d)
		return
	}
	var parts []playerPart
	for i, p := range version.Parts {
		item := m.ID
		if i > 0 {
			item = p.File.ID
		}
		pp := playerPart{Item: item, Source: version.ID, Duration: p.Info.Duration.Seconds()}
		if directPlay(p).OK {
			pp.Direct = "/web/stream/" + p.File.ID
		}
		if s.HLS != nil && remuxable(p).OK {
			pp.HLS = "/web/hls/" + p.File.ID + "/"
		}
		defAudio := p.Info.DefaultAudioIndex()
		for _, st := range p.Info.Streams {
			if st.Type == "Audio" {
				pp.Audio = append(pp.Audio, playerAudio{Index: st.Index, Label: st.DisplayTitle, Default: st.Index == defAudio})
			}
		}
		defSub := p.Info.DefaultSubtitleIndex()
		for _, st := range p.Info.Streams {
			if st.Type == "Subtitle" && st.IsTextSubtitleStream {
				pp.Tracks = append(pp.Tracks, playerTrack{Label: st.DisplayTitle, Lang: st.Language,
					Src: "/web/subtitles/" + p.File.ID + "/" + strconv.Itoa(st.Index), Default: st.Index == defSub})
			}
		}
		parts = append(parts, pp)
	}
	part, _ := strconv.Atoi(r.URL.Query().Get("part"))
	part = min(max(part, 0), len(parts)-1)
	start, _ := strconv.Atoi(r.URL.Query().Get("t"))
	config, err := json.Marshal(map[string]any{"parts": parts, "part": part, "start": max(start, 0), "csrf": sess.CSRF(),
		"back": "/web/movies/" + m.ID, "partLabel": s.text("part"), "busy": s.text("remuxBusy")})
	if err != nil {
		s.fail(w, err)
		return
	}
	// The player script reads it from a data attribute, which the template
	// escapes like any attribute value.
	d["Config"] = string(config)
	d["Poster"] = imageURL(m.ID, "Backdrop", images.Tag(m, images.Backdrop), 1280, 0)
	s.render(w, r, http.StatusOK, "play", sess, d)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request, sess *session) {
	toks, err := s.Store.Tokens(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	d := s.clientData(r, sess, "account")
	for k, v := range adminData(r, "") {
		d[k] = v
	}
	keys, err := s.passkeyRows(r, sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	d["Devices"], d["OwnSession"] = toks, sess.Token.SessionID
	d["Passkeys"], d["PasskeysHere"], d["QuickConnect"] = keys, s.passkeyOrigin(r) != "", s.QuickConnect != nil
	if s.Passkeys != nil {
		d["PasskeyOrigins"] = s.Passkeys.Origins()
	}
	d["DeviceName"] = deviceName(r.UserAgent())
	s.render(w, r, http.StatusOK, "account", sess, d)
}

func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	ok, err := auth.VerifyPassword(sess.User.PasswordHash, r.PostFormValue("current"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		back(w, r, "/web/account", "err", "currentWrong")
		return
	}
	pw, problem := newPassword(r)
	if problem != "" {
		back(w, r, "/web/account", "err", problem)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), sess.User.ID, hash); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("password changed", "user", sess.User.Name, "by", sess.User.Name)
	http.Redirect(w, r, "/web/login", http.StatusSeeOther)
}

func (s *Server) endOwnSession(w http.ResponseWriter, r *http.Request, sess *session) {
	err := s.Store.DeleteSession(r.Context(), sess.User.ID, r.PathValue("session"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	if r.PathValue("session") == sess.Token.SessionID {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	back(w, r, "/web/account", "ok", "sessionEnded")
}
