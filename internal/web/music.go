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

	"homeflix/internal/catalog"
	"homeflix/internal/hls"
	"homeflix/internal/images"
	"homeflix/internal/playstate"
	"homeflix/internal/store"
)

func (s *Server) musicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /web/music", s.signedIn(s.albums))
	mux.HandleFunc("GET /web/music/artists", s.signedIn(s.artists))
	mux.HandleFunc("GET /web/music/playlists", s.signedIn(s.playlists))
	mux.HandleFunc("GET /web/music/albums/{id}", s.signedIn(s.album))
	mux.HandleFunc("GET /web/music/artists/{id}", s.signedIn(s.artist))
	mux.HandleFunc("GET /web/music/playlists/{id}", s.signedIn(s.playlist))
	mux.HandleFunc("POST /web/music/playlists", s.signedIn(s.createPlaylist))
	mux.HandleFunc("POST /web/music/playlists/{id}/add", s.signedIn(s.addToPlaylist))
	mux.HandleFunc("POST /web/music/playlists/{id}/remove", s.signedIn(s.removeFromPlaylist))
	mux.HandleFunc("POST /web/music/playlists/{id}/delete", s.signedIn(s.deletePlaylist))
	mux.HandleFunc("POST /web/favorite/{id}", s.signedIn(s.toggleFavorite))
	mux.HandleFunc("GET /web/books", s.signedIn(s.books))
	mux.HandleFunc("GET /web/books/{id}", s.signedIn(s.book))
	mux.HandleFunc("GET /web/audio/{id}/index.m3u8", s.signedIn(s.audioPlaylist))
	mux.HandleFunc("GET /web/audio/{id}/{segment}", s.signedIn(s.audioSegment))
}

// hasLibrary reports whether a library of a collection type exists.
func (s *Server) hasLibrary(collectionType string) bool {
	for _, l := range s.snapshot().Libraries {
		if l.CollectionType == collectionType {
			return true
		}
	}
	return false
}

// audioTrack is a track or book as the audio player plays it.
type audioTrack struct {
	Item     string         `json:"item"` // play state is kept under it
	Title    string         `json:"title"`
	Artist   string         `json:"artist,omitempty"`
	Album    string         `json:"album,omitempty"`
	AlbumURL string         `json:"albumUrl,omitempty"`
	Cover    string         `json:"cover,omitempty"`
	Duration float64        `json:"duration"`
	Direct   string         `json:"direct,omitempty"` // the file, "" when it plays only converted
	MIME     string         `json:"mime,omitempty"`   // what the browser is asked whether it plays the file
	HLS      string         `json:"hls,omitempty"`    // the AAC conversion, "" when the user may not have it
	Book     bool           `json:"book,omitempty"`
	Start    float64        `json:"start,omitempty"` // where to begin, for books
	Chapters []audioChapter `json:"chapters,omitempty"`
}

type audioChapter struct {
	At   float64 `json:"at"`
	Name string  `json:"name"`
}

// mimeOf is the media type browsers are asked about for a file, "" for
// codecs no browser plays.
func mimeOf(m *catalog.Movie) string {
	info := m.Versions[0].Info()
	codec := ""
	for _, st := range info.Streams {
		if st.Type == "Audio" {
			codec = st.Codec
			break
		}
	}
	switch codec {
	case "mp3":
		return "audio/mpeg"
	case "aac":
		return `audio/mp4; codecs="mp4a.40.2"`
	case "alac":
		return `audio/mp4; codecs="alac"`
	case "flac":
		return "audio/flac"
	case "vorbis":
		return `audio/ogg; codecs="vorbis"`
	case "opus":
		return `audio/ogg; codecs="opus"`
	case "pcm_s16le", "pcm_s24le":
		return "audio/wav"
	}
	return ""
}

func coverURL(id, tag string, size int) string { return imageURL(id, "Primary", tag, size, size) }

func (s *Server) trackFor(m *catalog.Movie, sess *session, start time.Duration) audioTrack {
	v := m.Versions[0]
	t := audioTrack{Item: m.ID, Title: m.Title, Duration: v.Duration.Seconds()}
	single := len(v.Parts) == 1
	if mime := mimeOf(m); mime != "" && single {
		t.Direct, t.MIME = "/web/stream/"+v.Parts[0].File.ID, mime
	}
	if s.HLS != nil && sess.User.Conversion.Remux() {
		t.HLS = "/web/audio/" + m.ID + "/index.m3u8"
	}
	switch {
	case m.Track != nil:
		var names []string
		for _, a := range m.Track.Artists {
			names = append(names, a.Name)
		}
		t.Artist = strings.Join(names, ", ")
		if al := m.Track.Album; al != nil {
			t.Album, t.AlbumURL = al.Name, "/web/music/albums/"+al.ID
		}
		t.Cover = coverURL(m.ID, trackArtTag(m), 300)
	case m.Book != nil:
		b := m.Book
		t.Book, t.Duration = true, b.Duration.Seconds()
		var names []string
		for _, a := range b.Authors {
			names = append(names, a.Name)
		}
		t.Artist = strings.Join(names, ", ")
		t.Album, t.AlbumURL = b.Series, "/web/books/"+m.ID
		t.Cover = coverURL(m.ID, images.ArtTag(b.Cover), 300)
		t.Start = start.Seconds()
		for _, c := range b.Chapters {
			t.Chapters = append(t.Chapters, audioChapter{At: c.Start.Seconds(), Name: c.Name})
		}
	}
	return t
}

// trackArtTag is the tag of a track's picture: its album's cover, else
// its own.
func trackArtTag(m *catalog.Movie) string {
	if al := m.Track.Album; al != nil && !al.Cover.IsZero() {
		return images.ArtTag(al.Cover)
	}
	if c := m.Track.Meta.Cover; c >= 0 {
		f := m.Versions[0].Parts[0].File
		return images.ArtTag(catalog.Art{Embedded: f.Path, Stream: c, Stamp: f.ModTime.UnixMilli()})
	}
	return ""
}

func (s *Server) queueJSON(ms []*catalog.Movie, sess *session) string {
	list := make([]audioTrack, 0, len(ms))
	for _, m := range ms {
		if len(m.Versions) > 0 {
			list = append(list, s.trackFor(m, sess, 0))
		}
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// trackRow is a line of a track list.
type trackRow struct {
	Index         int // in the page's queue
	Disc, Number  int
	Title, Artist string
	Duration      string
	Played        bool
	Favorite      bool
	ID, Entry     string
}

func trackRows(ms []*catalog.Movie, user map[string]store.UserData, showArtist bool) []trackRow {
	var rows []trackRow
	for i, m := range ms {
		r := trackRow{Index: i, Title: m.Title, ID: m.ID, Played: user[m.ID].Played, Favorite: user[m.ID].IsFavorite}
		if len(m.Versions) > 0 {
			r.Duration = clock(m.Versions[0].Duration)
		}
		if t := m.Track; t != nil {
			r.Disc, r.Number = t.Disc, t.Number
			if showArtist || (t.Album != nil && len(t.Album.Artists) > 0 && len(t.Artists) > 0 && t.Artists[0] != t.Album.Artists[0]) {
				var names []string
				for _, a := range t.Artists {
					names = append(names, a.Name)
				}
				r.Artist = strings.Join(names, ", ")
			}
		}
		rows = append(rows, r)
	}
	return rows
}

func albumCard(al *catalog.Album, user map[string]store.UserData) card {
	c := card{URL: "/web/music/albums/" + al.ID, Image: coverURL(al.ID, images.ArtTag(al.Cover), 300), Title: al.Name}
	var parts []string
	if len(al.Artists) > 0 {
		parts = append(parts, al.Artists[0].Name)
	}
	if al.Year > 0 {
		parts = append(parts, strconv.Itoa(al.Year))
	}
	c.Sub = strings.Join(parts, " · ")
	return c
}

func artistCard(a *catalog.Artist) card {
	return card{URL: "/web/music/artists/" + a.ID, Image: coverURL(a.ID, images.ArtTag(a.Image("Primary")), 300), Title: a.Name}
}

func bookCard(m *catalog.Movie, user map[string]store.UserData) card {
	b := m.Book
	c := card{URL: "/web/books/" + m.ID, Image: coverURL(m.ID, images.ArtTag(b.Cover), 300), Title: m.Title}
	if len(b.Authors) > 0 {
		c.Sub = b.Authors[0].Name
	}
	d := user[m.ID]
	c.Played = d.Played
	if d.Position > 0 && b.Duration > 0 {
		c.Progress = int(math.Ceil(100 * float64(d.Position) / float64(b.Duration)))
	}
	return c
}

// gridPage reads the sort, letter and page of a grid and builds its links.
type gridPage struct {
	base    string
	q       url.Values
	Sorts   []link
	Letters []link
	All     link
	Page    int
	Prev    string
	Next    string
	Pages   int
}

func newGrid(r *http.Request, base string) *gridPage {
	g := &gridPage{base: base, q: r.URL.Query()}
	g.Page, _ = strconv.Atoi(g.q.Get("page"))
	g.Page = max(g.Page, 1)
	return g
}

func (g *gridPage) with(k, v string) string {
	n := url.Values{}
	for key, vals := range g.q {
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
		return g.base
	}
	return g.base + "?" + n.Encode()
}

// apply sets sort, letter and page on a query; sorts are key → SortBy.
func (g *gridPage) apply(q *catalog.AudioQuery, sorts [][2]string) {
	cur := g.q.Get("sort")
	for i, so := range sorts {
		on := so[0] == cur || (cur == "" && i == 0)
		if on {
			q.SortBy = strings.Split(so[1], ",")
			q.Descending = []bool{i > 0}
		}
		v := so[0]
		if i == 0 {
			v = ""
		}
		g.Sorts = append(g.Sorts, link{Label: so[0], URL: g.with("sort", v), Current: on})
	}
	letter := g.q.Get("letter")
	for _, l := range strings.Split("#ABCDEFGHIJKLMNOPQRSTUVWXYZ", "") {
		g.Letters = append(g.Letters, link{Label: l, URL: g.with("letter", l), Current: l == letter})
	}
	g.All = link{Label: "all", URL: g.with("letter", ""), Current: letter == ""}
	switch {
	case letter == "#":
		q.NameLessThan = "a"
	case len(letter) == 1:
		q.NameStartsWith = strings.ToLower(letter)
	}
	q.StartIndex, q.Limit = (g.Page-1)*pageSize, pageSize
}

func (g *gridPage) finish(total int) {
	if g.Page > 1 {
		g.Prev = g.with("page", strconv.Itoa(g.Page-1))
	}
	if g.Page*pageSize < total {
		g.Next = g.with("page", strconv.Itoa(g.Page+1))
	}
	g.Pages = (total + pageSize - 1) / pageSize
}

func (s *Server) musicData(r *http.Request, sess *session, tab string) map[string]any {
	d := s.clientData(r, sess, "music")
	d["Tab"] = tab
	return d
}

func (s *Server) albums(w http.ResponseWriter, r *http.Request, sess *session) {
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	snap := s.snapshot()
	q := catalog.AudioQuery{Types: map[string]bool{catalog.TypeAlbum: true}, User: user}
	g := newGrid(r, "/web/music")
	g.apply(&q, [][2]string{{"sortName", "SortName"}, {"sortAdded", "DateCreated"}, {"sortYear", "ProductionYear,SortName"}})
	if genre := r.URL.Query().Get("genre"); genre != "" {
		q.GenreIDs = []string{genre}
	}
	if r.URL.Query().Get("show") == "favorites" {
		q.Favorite = ptr(true)
	}
	items, total := snap.AudioItems(q)
	g.finish(total)
	var cards []card
	for _, it := range items {
		cards = append(cards, albumCard(it.Album, user))
	}
	d := s.musicData(r, sess, "albums")
	d["Cards"], d["Total"], d["Grid"] = cards, total, g
	if f, ok := snap.MusicGenre(r.URL.Query().Get("genre")); ok {
		d["Genre"], d["ClearGenre"] = f.Name, g.with("genre", "")
	}
	recent, _ := snap.AudioItems(catalog.AudioQuery{Types: map[string]bool{catalog.TypeAudio: true}, User: user,
		Played: ptr(true), SortBy: []string{"DatePlayed"}, Descending: []bool{true}, Limit: 12})
	if g.Page == 1 && r.URL.Query().Get("letter") == "" && len(recent) > 0 {
		var ms []*catalog.Movie
		for _, it := range recent {
			ms = append(ms, it.Item)
		}
		d["Recent"], d["Queue"] = trackRows(ms, user, true), s.queueJSON(ms, sess)
	}
	s.render(w, r, http.StatusOK, "music", sess, d)
}

func (s *Server) artists(w http.ResponseWriter, r *http.Request, sess *session) {
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	snap := s.snapshot()
	q := catalog.AudioQuery{Types: map[string]bool{catalog.TypeAlbumArtist: true}, User: user}
	if s.hasLibrary("books") {
		// Authors are artists too; the music page shows musicians.
		for _, l := range snap.Libraries {
			if l.CollectionType == "music" {
				q.LibraryID = l.ID
				break
			}
		}
	}
	g := newGrid(r, "/web/music/artists")
	g.apply(&q, [][2]string{{"sortName", "SortName"}, {"sortAdded", "DateCreated"}})
	items, total := snap.AudioItems(q)
	g.finish(total)
	var cards []card
	for _, it := range items {
		cards = append(cards, artistCard(it.Artist))
	}
	d := s.musicData(r, sess, "artists")
	d["Cards"], d["Total"], d["Grid"] = cards, total, g
	s.render(w, r, http.StatusOK, "music", sess, d)
}

func (s *Server) album(w http.ResponseWriter, r *http.Request, sess *session) {
	snap := s.snapshot()
	al, ok := snap.Album(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	d := s.musicData(r, sess, "albums")
	d["Album"], d["Cover"] = al, coverURL(al.ID, images.ArtTag(al.Cover), 600)
	d["Tracks"], d["Queue"] = trackRows(al.Tracks, user, false), s.queueJSON(al.Tracks, sess)
	d["Discs"] = len(al.Tracks) > 0 && al.Tracks[len(al.Tracks)-1].Track.Disc > 1
	d["Duration"], d["Favorite"] = clock(al.Duration), user[al.ID].IsFavorite
	var genres []link
	for _, g := range al.Genres {
		genres = append(genres, link{Label: g, URL: "/web/music?genre=" + catalog.MusicGenreID(g)})
	}
	d["Genres"] = genres
	var more []card
	for _, o := range snap.SimilarAlbums(al, 12) {
		more = append(more, albumCard(o, user))
	}
	d["Similar"] = more
	s.addPlaylistChoice(r, sess, d)
	s.render(w, r, http.StatusOK, "album", sess, d)
}

func (s *Server) artist(w http.ResponseWriter, r *http.Request, sess *session) {
	snap := s.snapshot()
	a, ok := snap.Artist(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	d := s.musicData(r, sess, "artists")
	d["Artist"], d["Picture"] = a, coverURL(a.ID, images.ArtTag(a.Image("Primary")), 600)
	if tag := images.ArtTag(a.Backdrop); tag != "" {
		d["Backdrop"] = imageURL(a.ID, "Backdrop", tag, 1280, 0)
	}
	var albums []card
	for _, al := range a.Albums {
		albums = append(albums, albumCard(al, user))
	}
	d["Albums"], d["Favorite"] = albums, user[a.ID].IsFavorite
	// Tracks on albums of others (features, compilations).
	var guest []*catalog.Movie
	for _, t := range a.Tracks {
		if t.Track.Album == nil || len(t.Track.Album.Artists) == 0 || t.Track.Album.Artists[0] != a {
			guest = append(guest, t)
		}
	}
	if len(guest) > 50 {
		guest = guest[:50]
	}
	d["Guest"], d["Queue"] = trackRows(guest, user, true), s.queueJSON(guest, sess)
	var all []*catalog.Movie
	for _, al := range a.Albums {
		all = append(all, al.Tracks...)
	}
	d["AllQueue"] = s.queueJSON(append(all, guest...), sess)
	var similar []card
	for _, o := range snap.SimilarArtists(a, 12) {
		similar = append(similar, artistCard(o))
	}
	d["Similar"] = similar
	var books []card
	for _, b := range a.Books {
		books = append(books, bookCard(b, user))
	}
	d["Books"] = books
	s.render(w, r, http.StatusOK, "artist", sess, d)
}

// addPlaylistChoice offers the user's playlists to add tracks to.
func (s *Server) addPlaylistChoice(r *http.Request, sess *session, d map[string]any) {
	list, err := s.Store.Playlists(r.Context(), sess.User.ID)
	if err != nil {
		return
	}
	var opts []link
	for _, p := range list {
		opts = append(opts, link{Label: p.Name, URL: p.ID})
	}
	d["Playlists"] = opts
}

func (s *Server) playlists(w http.ResponseWriter, r *http.Request, sess *session) {
	list, err := s.Store.Playlists(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	snap := s.snapshot()
	var cards []card
	for _, p := range list {
		c := card{URL: "/web/music/playlists/" + p.ID, Title: p.Name, Sub: strconv.Itoa(len(p.Entries))}
		for _, e := range p.Entries {
			if m, ok := snap.Item(e.ItemID); ok && m.Track != nil {
				if tag := trackArtTag(m); tag != "" {
					c.Image = coverURL(m.ID, tag, 300)
					break
				}
			}
		}
		cards = append(cards, c)
	}
	d := s.musicData(r, sess, "playlists")
	d["Cards"], d["Total"] = cards, len(cards)
	s.render(w, r, http.StatusOK, "music", sess, d)
}

func (s *Server) ownPlaylist(w http.ResponseWriter, r *http.Request, sess *session) (store.Playlist, bool) {
	p, err := s.Store.Playlist(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || err == nil && p.UserID != sess.User.ID {
		http.NotFound(w, r)
		return p, false
	}
	if err != nil {
		s.fail(w, err)
		return p, false
	}
	return p, true
}

func (s *Server) playlist(w http.ResponseWriter, r *http.Request, sess *session) {
	p, ok := s.ownPlaylist(w, r, sess)
	if !ok {
		return
	}
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	snap := s.snapshot()
	var ms []*catalog.Movie
	var entries []string
	for _, e := range p.Entries {
		if m, ok := snap.Item(e.ItemID); ok && (m.Track != nil || m.Book != nil) {
			ms = append(ms, m)
			entries = append(entries, e.ID)
		}
	}
	rows := trackRows(ms, user, true)
	for i := range rows {
		rows[i].Entry = entries[i]
	}
	d := s.musicData(r, sess, "playlists")
	d["Playlist"], d["Tracks"], d["Queue"] = p, rows, s.queueJSON(ms, sess)
	d["Missing"] = len(p.Entries) - len(ms)
	s.render(w, r, http.StatusOK, "playlist", sess, d)
}

// formIDs reads the item IDs of a form: "ids", comma-separated or
// repeated.
func formIDs(r *http.Request) []string {
	var out []string
	for _, v := range r.PostForm["ids"] {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func (s *Server) createPlaylist(w http.ResponseWriter, r *http.Request, sess *session) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		http.Redirect(w, r, safeBack(r.PostFormValue("back"), "/web/music/playlists"), http.StatusSeeOther)
		return
	}
	p, err := s.Store.CreatePlaylist(r.Context(), sess.User.ID, name, formIDs(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/web/music/playlists/"+p.ID, http.StatusSeeOther)
}

func (s *Server) addToPlaylist(w http.ResponseWriter, r *http.Request, sess *session) {
	p, ok := s.ownPlaylist(w, r, sess)
	if !ok {
		return
	}
	if err := s.Store.AddToPlaylist(r.Context(), p.ID, formIDs(r)); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/web/music/playlists/"+p.ID, http.StatusSeeOther)
}

func (s *Server) removeFromPlaylist(w http.ResponseWriter, r *http.Request, sess *session) {
	p, ok := s.ownPlaylist(w, r, sess)
	if !ok {
		return
	}
	if err := s.Store.RemoveFromPlaylist(r.Context(), p.ID, []string{r.PostFormValue("entry")}); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/web/music/playlists/"+p.ID, http.StatusSeeOther)
}

func (s *Server) deletePlaylist(w http.ResponseWriter, r *http.Request, sess *session) {
	p, ok := s.ownPlaylist(w, r, sess)
	if !ok {
		return
	}
	if err := s.Store.DeletePlaylist(r.Context(), p.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/web/music/playlists", http.StatusSeeOther)
}

// safeBack accepts a page of the web client to return to.
func safeBack(back, def string) string {
	if strings.HasPrefix(back, "/web/") && !strings.HasPrefix(back, "//") && !strings.ContainsAny(back, "\\\r\n") {
		return back
	}
	return def
}

// toggleFavorite sets the favourite mark of an album, artist, track or
// book and returns to the page it came from.
func (s *Server) toggleFavorite(w http.ResponseWriter, r *http.Request, sess *session) {
	snap := s.snapshot()
	id := r.PathValue("id")
	_, isAlbum := snap.Album(id)
	_, isArtist := snap.Artist(id)
	m, isItem := snap.Item(id)
	if !isAlbum && !isArtist && !(isItem && (m.Track != nil || m.Book != nil)) {
		http.NotFound(w, r)
		return
	}
	d, err := s.Store.GetUserData(r.Context(), sess.User.ID, id)
	if err == nil {
		d.IsFavorite = r.PostFormValue("favorite") == "on"
		err = s.Store.SaveUserData(r.Context(), sess.User.ID, d)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, safeBack(r.PostFormValue("back"), "/web/music"), http.StatusSeeOther)
}

func (s *Server) books(w http.ResponseWriter, r *http.Request, sess *session) {
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	snap := s.snapshot()
	q := catalog.AudioQuery{Types: map[string]bool{catalog.TypeAudioBook: true}, User: user}
	g := newGrid(r, "/web/books")
	g.apply(&q, [][2]string{{"sortName", "SortName"}, {"sortAdded", "DateCreated"}, {"sortAuthor", "AlbumArtist,SortName"}})
	if len(g.Sorts) == 3 && g.Sorts[2].Current {
		q.Descending = []bool{false}
	}
	if a := r.URL.Query().Get("author"); a != "" {
		q.ArtistIDs = []string{a}
	}
	switch r.URL.Query().Get("show") {
	case "unplayed":
		q.Played = ptr(false)
	case "favorites":
		q.Favorite = ptr(true)
	}
	items, total := snap.AudioItems(q)
	g.finish(total)
	var cards []card
	for _, it := range items {
		cards = append(cards, bookCard(it.Item, user))
	}
	d := s.clientData(r, sess, "books")
	d["Cards"], d["Total"], d["Grid"] = cards, total, g
	if a, ok := snap.Artist(r.URL.Query().Get("author")); ok {
		d["Author"], d["ClearAuthor"] = a.Name, g.with("author", "")
	}
	if g.Page == 1 {
		var cont []card
		for _, res := range snap.Resume(user, "", 0) {
			if res.Movie.Book != nil && len(cont) < 12 {
				cont = append(cont, bookCard(res.Movie, user))
			}
		}
		d["Continue"] = cont
	}
	s.render(w, r, http.StatusOK, "books", sess, d)
}

func (s *Server) book(w http.ResponseWriter, r *http.Request, sess *session) {
	snap := s.snapshot()
	m, ok := snap.Item(r.PathValue("id"))
	if !ok || m.Book == nil || len(m.Versions) == 0 {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess)
	if !ok {
		return
	}
	b := m.Book
	ud := user[m.ID]
	t := s.trackFor(m, sess, ud.Position)
	q, _ := json.Marshal([]audioTrack{t})
	d := s.clientData(r, sess, "books")
	d["Book"], d["Item"], d["Cover"], d["Queue"] = b, m, coverURL(m.ID, images.ArtTag(b.Cover), 600), string(q)
	d["Duration"], d["Favorite"], d["Played"] = clock(b.Duration), ud.IsFavorite, ud.Played
	if ud.Position > 0 {
		d["ResumeAt"], d["ResumeSeconds"] = clock(ud.Position), int(ud.Position.Seconds())
		if b.Duration > 0 {
			d["Progress"] = int(math.Ceil(100 * float64(ud.Position) / float64(b.Duration)))
		}
	}
	type chapter struct {
		Name, At string
		Seconds  int
	}
	var chapters []chapter
	for _, c := range b.Chapters {
		chapters = append(chapters, chapter{Name: c.Name, At: clock(c.Start), Seconds: int(c.Start.Seconds())})
	}
	d["Chapters"] = chapters
	var more []card
	for _, a := range b.Authors {
		for _, o := range a.Books {
			if o != m && len(more) < 12 {
				more = append(more, bookCard(o, user))
			}
		}
	}
	d["More"] = more
	s.render(w, r, http.StatusOK, "book", sess, d)
}

// audioSource describes the AAC conversion of a track or a whole book.
func audioSource(m *catalog.Movie, owner string) (hls.Source, bool) {
	if len(m.Versions) == 0 {
		return hls.Source{}, false
	}
	v := m.Versions[0]
	first := v.Parts[0]
	audio := first.Info.DefaultAudioIndex()
	if audio < 0 {
		return hls.Source{}, false
	}
	src := hls.Source{FileID: first.File.ID, Path: first.File.Path, Size: first.File.Size, ModTime: first.File.ModTime,
		Duration: v.Duration, Audio: audio, AudioOnly: true, Owner: owner}
	var key []string
	for _, p := range v.Parts {
		key = append(key, fmt.Sprintf("%s-%d-%d", p.File.ID, p.File.Size, p.File.ModTime.UnixMilli()))
	}
	src.Key = strings.Join(key, "+")
	if len(v.Parts) > 1 {
		for _, p := range v.Parts {
			src.Concat = append(src.Concat, p.File.Path)
		}
		src.Key = fmt.Sprintf("book-%s-%d-%s", m.ID, len(v.Parts), key[len(key)-1])
	}
	return src, true
}

func (s *Server) audioFor(w http.ResponseWriter, r *http.Request, sess *session) (*catalog.Movie, hls.Source, bool) {
	if s.HLS == nil {
		http.NotFound(w, r)
		return nil, hls.Source{}, false
	}
	if !sess.User.Conversion.Remux() {
		http.Error(w, "this account plays music and audiobooks only as they are", http.StatusForbidden)
		return nil, hls.Source{}, false
	}
	m, ok := s.snapshot().Item(r.PathValue("id"))
	if !ok || m.Track == nil && m.Book == nil {
		http.NotFound(w, r)
		return nil, hls.Source{}, false
	}
	src, ok := audioSource(m, sess.Token.SessionID)
	if !ok {
		http.NotFound(w, r)
		return nil, hls.Source{}, false
	}
	return m, src, true
}

func (s *Server) audioPlaylist(w http.ResponseWriter, r *http.Request, sess *session) {
	m, src, ok := s.audioFor(w, r, sess)
	if !ok {
		return
	}
	segs, err := s.HLS.Segments(r.Context(), src)
	if err != nil {
		http.Error(w, "this file cannot be converted", http.StatusUnprocessableEntity)
		return
	}
	s.Log.Info("audio playlist", "item", m.Title, "user", sess.User.Name, "client", clientName)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(hls.Playlist(segs, func(i int) string { return strconv.Itoa(i) + ".ts" }))
}

func (s *Server) audioSegment(w http.ResponseWriter, r *http.Request, sess *session) {
	_, src, ok := s.audioFor(w, r, sess)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("segment"), ".ts"))
	if err != nil || !strings.HasSuffix(r.PathValue("segment"), ".ts") {
		http.NotFound(w, r)
		return
	}
	path, err := s.HLS.Segment(r.Context(), src, n)
	switch {
	case errors.Is(err, hls.ErrBusy):
		w.Header().Set("Retry-After", "10")
		http.Error(w, "the server is busy converting other files", http.StatusServiceUnavailable)
		return
	case err != nil:
		if r.Context().Err() == nil {
			s.Log.Warn("segment failed", "path", src.Path, "segment", n, "err", err)
			http.Error(w, "segment not available", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

// audioRuntime is what positions of a track or book refer to.
func audioRuntime(m *catalog.Movie) time.Duration { return playstate.Runtime(m) }
