package jellyfin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/images"
	"homeflix/internal/media"
	"homeflix/internal/store"
)

func (s *Server) registerPlaylists(rt *Router) {
	rt.HandleFunc(http.MethodPost, "/Playlists", s.authed(s.createPlaylist))
	rt.HandleFunc(http.MethodGet, "/Playlists/{id}", s.authed(s.getPlaylist))
	rt.HandleFunc(http.MethodPost, "/Playlists/{id}", s.authed(s.updatePlaylist))
	rt.HandleFunc(http.MethodGet, "/Playlists/{id}/Items", s.authed(s.playlistItems))
	rt.HandleFunc(http.MethodPost, "/Playlists/{id}/Items", s.authed(s.addToPlaylist))
	rt.HandleFunc(http.MethodDelete, "/Playlists/{id}/Items", s.authed(s.removeFromPlaylist))
	rt.HandleFunc(http.MethodPost, "/Playlists/{id}/Items/{entry}/Move/{index}", s.authed(s.movePlaylistEntry))
	rt.HandleFunc(http.MethodDelete, "/Items/{itemId}", s.authed(s.deleteItem))

	rt.HandleFunc(http.MethodGet, "/Audio/{itemId}/Lyrics", s.authed(s.lyrics))
	for _, p := range []string{"/Items/{itemId}/InstantMix", "/Songs/{itemId}/InstantMix", "/Albums/{itemId}/InstantMix",
		"/Playlists/{itemId}/InstantMix", "/Artists/{itemId}/InstantMix", "/MusicGenres/{itemId}/InstantMix"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.instantMix))
	}
	rt.HandleFunc(http.MethodGet, "/Artists/InstantMix", s.authed(s.instantMix))
	rt.HandleFunc(http.MethodGet, "/MusicGenres/InstantMix", s.authed(s.instantMix))

	// Moonfin keeps bookmarks and notes of audiobooks through its own
	// server plugin; homeflix stores what the app sends.
	rt.HandleFunc(http.MethodGet, "/Moonfin/Bookmarks/{itemId}", s.authed(s.getBookmarks))
	rt.HandleFunc(http.MethodPost, "/Moonfin/Bookmarks/{itemId}", s.authed(s.setBookmarks("bookmarks")))
	rt.HandleFunc(http.MethodPost, "/Moonfin/Bookmarks/{itemId}/Notes", s.authed(s.setBookmarks("notes")))
}

// ownPlaylist loads a playlist of the signed-in user; it writes the error
// response and returns false otherwise. Administrators may see every
// playlist, as in Jellyfin.
func (s *Server) ownPlaylist(w http.ResponseWriter, r *http.Request, sess *Session, id string) (store.Playlist, bool) {
	p, err := s.Store.Playlist(r.Context(), normalizeID(id))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return p, false
	case err != nil:
		s.internalError(w, r, err)
		return p, false
	case p.UserID != sess.User.ID && !sess.User.IsAdmin:
		http.NotFound(w, r)
		return p, false
	}
	return p, true
}

// playlistTracks resolves the entries of a playlist; entries of items no
// longer in the library are left out.
func playlistTracks(snap *catalog.Snapshot, p store.Playlist) ([]*catalog.Movie, []store.PlaylistEntry) {
	var items []*catalog.Movie
	var entries []store.PlaylistEntry
	for _, e := range p.Entries {
		if m, ok := snap.Item(e.ItemID); ok {
			items = append(items, m)
			entries = append(entries, e)
		}
	}
	return items, entries
}

func (s *Server) playlistDto(p store.Playlist, user map[string]store.UserData) BaseItemDto {
	snap := s.Catalog.Snapshot()
	items, _ := playlistTracks(snap, p)
	d := s.audioBase(p.ID, p.Name, strings.ToLower(p.Name), "Playlist")
	d.IsFolder = true
	d.DateCreated = timePtr(p.Created)
	n := len(items)
	d.ChildCount, d.RecursiveItemCount = &n, &n
	d.MediaType = "Audio"
	var runtime time.Duration
	for _, m := range items {
		if !isAudioItem(m) {
			d.MediaType = "Video"
		}
		if len(m.Versions) > 0 {
			runtime += runtimeOf(m)
		}
	}
	if n == 0 {
		d.MediaType = "Audio"
	}
	d.RunTimeTicks = ticks(runtime)
	if art, ok := playlistArt(snap, p); ok {
		d.ImageTags[string(images.Primary)] = images.ArtTag(art)
		d.PrimaryImageAspectRatio = 1
	}
	d.UserData = groupUserData(p.ID, items, user)
	d.Path = ""
	return d
}

// playlistArt is the cover of the first entry that has one.
func playlistArt(snap *catalog.Snapshot, p store.Playlist) (catalog.Art, bool) {
	items, _ := playlistTracks(snap, p)
	for _, m := range items {
		switch {
		case m.Track != nil:
			if a := trackCover(m); !a.IsZero() {
				return a, true
			}
		case m.Book != nil:
			if !m.Book.Cover.IsZero() {
				return m.Book.Cover, true
			}
		}
	}
	return catalog.Art{}, false
}

// readBody decodes an optional JSON body into v.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

func idList(r *http.Request, name string, body []string) []string {
	var out []string
	for _, id := range append(listParam(r, name), body...) {
		for _, p := range strings.Split(id, ",") {
			if p = normalizeID(strings.TrimSpace(p)); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func (s *Server) createPlaylist(w http.ResponseWriter, r *http.Request, sess *Session) {
	var body struct {
		Name string
		Ids  []string
	}
	if !readBody(w, r, &body) {
		return
	}
	name := body.Name
	if name == "" {
		name = queryValue(r, "Name")
	}
	if strings.TrimSpace(name) == "" {
		http.Error(w, "a playlist needs a name", http.StatusBadRequest)
		return
	}
	p, err := s.Store.CreatePlaylist(r.Context(), sess.User.ID, strings.TrimSpace(name), idList(r, "Ids", body.Ids))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.Log.Info("playlist created", "name", p.Name, "entries", len(p.Entries), "user", sess.User.Name)
	writeJSON(w, http.StatusOK, map[string]string{"Id": p.ID})
}

func (s *Server) getPlaylist(w http.ResponseWriter, r *http.Request, sess *Session) {
	p, ok := s.ownPlaylist(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	ids := []string{}
	for _, e := range p.Entries {
		ids = append(ids, e.ItemID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"OpenAccess": false, "Shares": []any{}, "ItemIds": ids})
}

func (s *Server) updatePlaylist(w http.ResponseWriter, r *http.Request, sess *Session) {
	p, ok := s.ownPlaylist(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		Name *string
		Ids  []string
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		if err := s.Store.RenamePlaylist(r.Context(), p.ID, strings.TrimSpace(*body.Name)); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) playlistItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	p, ok := s.ownPlaylist(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	user, ok := s.userState(w, r, sess.User)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.playlistEntries(r, sess, p, user))
}

// playlistEntries lists the entries of a playlist as items with their
// PlaylistItemId, paged as the request asks.
func (s *Server) playlistEntries(r *http.Request, sess *Session, p store.Playlist, user map[string]store.UserData) QueryResult[BaseItemDto] {
	items, entries := playlistTracks(s.Catalog.Snapshot(), p)
	fields := parseFields(queryValue(r, "Fields"))
	var out []BaseItemDto
	for i, m := range items {
		d := s.itemDto(m, fields, sess, user)
		d.PlaylistItemID = entries[i].ID
		out = append(out, d)
	}
	return page(out, intParam(r, "StartIndex", 0), intParam(r, "Limit", 0))
}

func (s *Server) addToPlaylist(w http.ResponseWriter, r *http.Request, sess *Session) {
	p, ok := s.ownPlaylist(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.Store.AddToPlaylist(r.Context(), p.ID, idList(r, "Ids", nil)); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeFromPlaylist(w http.ResponseWriter, r *http.Request, sess *Session) {
	p, ok := s.ownPlaylist(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.Store.RemoveFromPlaylist(r.Context(), p.ID, idList(r, "EntryIds", nil)); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) movePlaylistEntry(w http.ResponseWriter, r *http.Request, sess *Session) {
	p, ok := s.ownPlaylist(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}
	switch err := s.Store.MovePlaylistEntry(r.Context(), p.ID, normalizeID(r.PathValue("entry")), index); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteItem deletes a playlist; library items are never deleted
// through the API.
func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request, sess *Session) {
	p, ok := s.ownPlaylist(w, r, sess, r.PathValue("itemId"))
	if !ok {
		return
	}
	if err := s.Store.DeletePlaylist(r.Context(), p.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.Log.Info("playlist deleted", "name", p.Name, "user", sess.User.Name)
	w.WriteHeader(http.StatusNoContent)
}

// userPlaylists lists the user's playlists as items.
func (s *Server) userPlaylists(w http.ResponseWriter, r *http.Request, u store.User, user map[string]store.UserData) {
	list, err := s.Store.Playlists(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	search := catalog.SearchKey(queryValue(r, "SearchTerm"))
	var out []BaseItemDto
	for _, p := range list {
		if search != "" && !strings.Contains(catalog.SearchKey(p.Name), search) {
			continue
		}
		out = append(out, s.playlistDto(p, user))
	}
	writeJSON(w, http.StatusOK, page(out, intParam(r, "StartIndex", 0), intParam(r, "Limit", 0)))
}

// LyricDto is a track's lyrics as Jellyfin returns them.
type LyricDto struct {
	Metadata map[string]any `json:"Metadata"`
	Lyrics   []LyricLineDto `json:"Lyrics"`
}

// LyricLineDto is one line; Start is in ticks, absent without times.
type LyricLineDto struct {
	Text  string `json:"Text"`
	Start *int64 `json:"Start,omitempty"`
}

// lyrics serves the lyrics of a track: its .lrc file, else its lyrics tag.
func (s *Server) lyrics(w http.ResponseWriter, r *http.Request, sess *Session) {
	m, ok := s.Catalog.Snapshot().Item(normalizeID(r.PathValue("itemId")))
	if !ok || m.Track == nil {
		http.NotFound(w, r)
		return
	}
	var text string
	if p := m.Track.Lyrics; p != "" {
		b, err := os.ReadFile(p)
		if err == nil && len(b) <= 1<<20 {
			text = string(b)
		}
	}
	if strings.TrimSpace(text) == "" && m.Track.Meta.Lyrics {
		probe, err := s.Store.FileProbe(r.Context(), m.Versions[0].Parts[0].File.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		text = media.EmbeddedLyrics(probe)
	}
	l := media.ParseLyrics(text)
	if len(l.Lines) == 0 {
		http.NotFound(w, r)
		return
	}
	out := LyricDto{Metadata: map[string]any{"IsSynced": l.Lines[0].Start != nil}, Lyrics: []LyricLineDto{}}
	if l.Artist != "" {
		out.Metadata["Artist"] = l.Artist
	}
	if l.Title != "" {
		out.Metadata["Title"] = l.Title
	}
	if l.Album != "" {
		out.Metadata["Album"] = l.Album
	}
	if l.Length > 0 {
		out.Metadata["Length"] = ticks(l.Length)
	}
	for _, ln := range l.Lines {
		d := LyricLineDto{Text: ln.Text}
		if ln.Start != nil {
			t := ticks(*ln.Start)
			d.Start = &t
		}
		out.Lyrics = append(out.Lyrics, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// instantMix lists tracks that go with an item, artist or genre.
func (s *Server) instantMix(w http.ResponseWriter, r *http.Request, sess *Session) {
	user, ok := s.userState(w, r, sess.User)
	if !ok {
		return
	}
	snap := s.Catalog.Snapshot()
	id := normalizeID(r.PathValue("itemId"))
	if id == "" {
		id = normalizeID(queryValue(r, "id"))
	}
	seeds := []string{id}
	if strings.HasPrefix(strings.ToLower(r.URL.Path), "/musicgenres/") && r.PathValue("itemId") != "" {
		if _, ok := snap.MusicGenre(id); !ok {
			seeds = []string{catalog.MusicGenreID(r.PathValue("itemId"))}
		}
	}
	if p, err := s.Store.Playlist(r.Context(), id); err == nil && (p.UserID == sess.User.ID || sess.User.IsAdmin) {
		seeds = nil
		for _, e := range p.Entries {
			seeds = append(seeds, e.ItemID)
		}
	}
	fields := parseFields(queryValue(r, "Fields"))
	items := []BaseItemDto{}
	for _, m := range snap.InstantMix(seeds, intParam(r, "Limit", 50)) {
		items = append(items, s.itemDto(m, fields, sess, user))
	}
	writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: items, TotalRecordCount: len(items)})
}

func (s *Server) getBookmarks(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := normalizeID(r.PathValue("itemId"))
	out := map[string]json.RawMessage{"bookmarks": json.RawMessage("[]"), "notes": json.RawMessage("[]")}
	for _, kind := range []string{"bookmarks", "notes"} {
		data, err := s.Store.Bookmarks(r.Context(), sess.User.ID, id, kind)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if data != "" {
			out[kind] = json.RawMessage(data)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// setBookmarks stores the list the app sends; it must be a JSON array.
func (s *Server) setBookmarks(kind string) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *Session) {
		var list []json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&list); err != nil {
			http.Error(w, "expected a JSON array", http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(list)
		if err := s.Store.SetBookmarks(r.Context(), sess.User.ID, normalizeID(r.PathValue("itemId")), kind, string(b)); err != nil {
			s.internalError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
