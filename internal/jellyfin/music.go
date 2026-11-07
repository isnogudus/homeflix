package jellyfin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/images"
	"homeflix/internal/store"
)

func (s *Server) registerMusic(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/Artists", s.authed(s.artists(false)))
	rt.HandleFunc(http.MethodGet, "/Artists/AlbumArtists", s.authed(s.artists(true)))
	rt.HandleFunc(http.MethodGet, "/Artists/{name}", s.authed(s.artistByName))
	rt.HandleFunc(http.MethodGet, "/MusicGenres", s.authed(s.musicGenres("MusicGenre")))
	rt.HandleFunc(http.MethodGet, "/MusicGenres/{name}", s.authed(s.genreByName("MusicGenre")))
	rt.HandleFunc(http.MethodGet, "/Genres/{name}", s.authed(s.genreByName("Genre")))
	rt.HandleFunc(http.MethodGet, "/Audio/{itemId}/stream", s.authed(s.audioStream))
	rt.HandleFunc(http.MethodGet, "/Audio/{itemId}/stream.{container}", s.authed(s.audioStream))
}

// audioTypeNames maps Jellyfin item type names to the catalog's.
var audioTypeNames = map[string]string{
	"audio": catalog.TypeAudio, "musicalbum": catalog.TypeAlbum, "musicartist": catalog.TypeArtist,
	"albumartist": catalog.TypeAlbumArtist, "audiobook": catalog.TypeAudioBook, "book": catalog.TypeAudioBook,
}

// audioTypes reads the audio item types a request asks for.
func audioTypes(r *http.Request) map[string]bool {
	types := map[string]bool{}
	for _, t := range listParam(r, "IncludeItemTypes") {
		if n, ok := audioTypeNames[strings.ToLower(t)]; ok {
			types[n] = true
		}
	}
	for _, t := range listParam(r, "ExcludeItemTypes") {
		if n, ok := audioTypeNames[strings.ToLower(t)]; ok {
			delete(types, n)
		}
	}
	return types
}

// isAudioLibrary reports whether a library holds music or audiobooks.
func isAudioLibrary(l catalog.Library) bool {
	return l.CollectionType == "music" || l.CollectionType == "books"
}

// wantsAudio reports whether an item listing is about music or
// audiobooks: it asks for their types, names an album or an audio library
// as parent, or filters by artist.
func wantsAudio(r *http.Request, snap *catalog.Snapshot, parent string) bool {
	if len(audioTypes(r)) > 0 {
		return true
	}
	if parent != "" {
		if _, ok := snap.Album(parent); ok {
			return true
		}
		if _, ok := snap.Artist(parent); ok {
			return true
		}
		if l, ok := snap.Library(parent); ok && isAudioLibrary(l) {
			return true
		}
	}
	for _, name := range []string{"ArtistIds", "AlbumArtistIds", "ContributingArtistIds"} {
		if len(listParam(r, name)) > 0 {
			return true
		}
	}
	return false
}

// audioQuery builds the catalog query of a music or audiobook listing.
// ok is false when the request can have no results.
func audioQuery(r *http.Request, snap *catalog.Snapshot, parent string, user map[string]store.UserData) (catalog.AudioQuery, bool) {
	q := catalog.AudioQuery{
		Types:                   audioTypes(r),
		Search:                  queryValue(r, "SearchTerm"),
		NameStartsWith:          queryValue(r, "NameStartsWith"),
		NameStartsWithOrGreater: queryValue(r, "NameStartsWithOrGreater"),
		NameLessThan:            queryValue(r, "NameLessThan"),
		SortBy:                  listParam(r, "SortBy"),
		StartIndex:              intParam(r, "StartIndex", 0),
		Limit:                   intParam(r, "Limit", 0),
		User:                    user,
	}
	for _, o := range listParam(r, "SortOrder") {
		q.Descending = append(q.Descending, strings.EqualFold(o, "Descending"))
	}
	for _, id := range listParam(r, "Ids") {
		q.IDs = append(q.IDs, normalizeID(id))
	}
	for _, name := range []string{"ArtistIds", "ContributingArtistIds"} {
		for _, id := range listParam(r, name) {
			q.ArtistIDs = append(q.ArtistIDs, normalizeID(id))
		}
	}
	for _, id := range listParam(r, "AlbumArtistIds") {
		q.AlbumArtistIDs = append(q.AlbumArtistIDs, normalizeID(id))
	}
	for _, g := range listParam(r, "Genres") {
		q.GenreIDs = append(q.GenreIDs, catalog.MusicGenreID(g))
	}
	for _, id := range listParam(r, "GenreIds") {
		q.GenreIDs = append(q.GenreIDs, normalizeID(id))
	}
	for _, y := range listParam(r, "Years") {
		if n, err := strconv.Atoi(y); err == nil {
			q.Years = append(q.Years, n)
		}
	}
	for name, dst := range map[string]*time.Time{"MinPremiereDate": &q.MinPremiere, "MaxPremiereDate": &q.MaxPremiere} {
		if v := queryValue(r, name); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				*dst = t
			} else if t, err := time.Parse("2006-01-02", firstN(v, 10)); err == nil {
				*dst = t
			}
		}
	}
	var pq catalog.Query
	playStateFilters(r, &pq)
	q.Played, q.Favorite, q.Resumable = pq.Played, pq.Favorite, pq.Resumable

	recursive := boolValue(boolParam(r, "Recursive"))
	if parent != "" {
		switch {
		case func() bool { _, ok := snap.Album(parent); return ok }():
			q.ParentID = parent
			if len(q.Types) == 0 {
				q.Types = map[string]bool{catalog.TypeAudio: true}
			}
		case func() bool { _, ok := snap.Artist(parent); return ok }():
			q.ArtistIDs = append(q.ArtistIDs, parent)
			if len(q.Types) == 0 {
				q.Types = map[string]bool{catalog.TypeAlbum: true}
			}
		default:
			l, ok := snap.Library(parent)
			if !ok {
				return q, false
			}
			// The direct children of a library are folders, which homeflix
			// does not list; Moonfin asks for them to queue the "siblings"
			// of a book and must get none, not the whole library.
			if !recursive && q.Search == "" && len(q.IDs) == 0 {
				return q, false
			}
			q.LibraryID = l.ID
			if len(q.Types) == 0 {
				if l.CollectionType == "books" {
					q.Types = map[string]bool{catalog.TypeAudioBook: true}
				} else {
					q.Types = map[string]bool{catalog.TypeAlbum: true}
				}
			}
		}
	}
	if len(q.Types) == 0 {
		q.Types = map[string]bool{catalog.TypeAlbum: true, catalog.TypeAudio: true}
	}
	return q, true
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// audioItems answers an item listing about music or audiobooks.
func (s *Server) audioItems(w http.ResponseWriter, r *http.Request, sess *Session, snap *catalog.Snapshot,
	user map[string]store.UserData, fields fieldSet, parent string) {
	start := intParam(r, "StartIndex", 0)
	q, ok := audioQuery(r, snap, parent, user)
	if !ok {
		writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: []BaseItemDto{}, StartIndex: start})
		return
	}
	items, total := snap.AudioItems(q)
	if limitZero(r) {
		// Moonfin counts songs with Limit=0: the total, no items.
		items = nil
	}
	out := QueryResult[BaseItemDto]{Items: make([]BaseItemDto, 0, len(items)), TotalRecordCount: total, StartIndex: start}
	for _, it := range items {
		out.Items = append(out.Items, s.audioItemDto(it, fields, sess, user))
	}
	writeJSON(w, http.StatusOK, out)
}

// limitZero reports whether a request asks for no items, only their count.
func limitZero(r *http.Request) bool {
	v, ok := r.URL.Query()["Limit"]
	if !ok {
		for k, vs := range r.URL.Query() {
			if strings.EqualFold(k, "Limit") {
				v, ok = vs, true
			}
		}
	}
	return ok && len(v) > 0 && strings.TrimSpace(v[0]) == "0"
}

func (s *Server) audioItemDto(it catalog.AudioItem, fields fieldSet, sess *Session, user map[string]store.UserData) BaseItemDto {
	switch {
	case it.Album != nil:
		return s.albumDto(it.Album, fields, user)
	case it.Artist != nil:
		return s.artistDto(it.Artist, fields, user)
	}
	return s.itemDto(it.Item, fields, sess, user)
}

// audioBase holds the fields every music and audiobook item has.
func (s *Server) audioBase(id, name, sortName, typ string) BaseItemDto {
	return BaseItemDto{
		Name:              name,
		ServerID:          s.ServerID,
		ID:                id,
		SortName:          sortName,
		ExternalUrls:      []ExternalURL{},
		Taglines:          []string{},
		Genres:            []string{},
		ProviderIDs:       map[string]string{},
		Type:              typ,
		People:            []PersonDto{},
		Studios:           []NameIDPair{},
		GenreItems:        []NameIDPair{},
		Tags:              []string{},
		ImageTags:         map[string]string{},
		BackdropImageTags: []string{},
		ImageBlurHashes:   map[string]map[string]string{},
		LocationType:      "FileSystem",
		MediaType:         "Unknown",
		RemoteTrailers:    []any{},
		PlayAccess:        "Full",
	}
}

func artistPairs(list []*catalog.Artist) ([]NameIDPair, []string) {
	pairs := []NameIDPair{}
	names := []string{}
	for _, a := range list {
		pairs = append(pairs, NameIDPair{Name: a.Name, ID: a.ID})
		names = append(names, a.Name)
	}
	return pairs, names
}

func setGenres(d *BaseItemDto, genres []string) {
	d.Genres = append([]string{}, genres...)
	for _, g := range genres {
		d.GenreItems = append(d.GenreItems, NameIDPair{Name: g, ID: catalog.MusicGenreID(g)})
	}
}

func musicBrainzURL(kind, id string) ExternalURL {
	return ExternalURL{Name: "MusicBrainz", URL: "https://musicbrainz.org/" + kind + "/" + id}
}

// artistImages adds the backdrop and logo of the first artist with them,
// as Jellyfin does for albums and tracks.
func artistImages(d *BaseItemDto, artists []*catalog.Artist) {
	for _, a := range artists {
		if tag := images.ArtTag(a.Backdrop); tag != "" && d.ParentBackdropItemID == "" {
			d.ParentBackdropItemID, d.ParentBackdropImageTags = a.ID, []string{tag}
		}
		if tag := images.ArtTag(a.Logo); tag != "" && d.ParentLogoItemID == "" {
			d.ParentLogoItemID, d.ParentLogoImageTag = a.ID, tag
		}
	}
}

// trackCover is the picture shown for a track: its album's cover, else
// the picture in the track itself.
func trackCover(m *catalog.Movie) catalog.Art {
	if al := m.Track.Album; al != nil && !al.Cover.IsZero() {
		return al.Cover
	}
	if c := m.Track.Meta.Cover; c >= 0 {
		f := m.Versions[0].Parts[0].File
		return catalog.Art{Embedded: f.Path, Stream: c, Stamp: f.ModTime.UnixMilli()}
	}
	return catalog.Art{}
}

func (s *Server) trackDto(m *catalog.Movie, fields fieldSet, sess *Session, user map[string]store.UserData) BaseItemDto {
	t := m.Track
	d := s.audioBase(m.ID, m.Title, m.SortTitle, "Audio")
	d.MediaType = "Audio"
	d.DateCreated = timePtr(m.DateCreated)
	d.ProductionYear = m.Year
	d.PremiereDate = timePtr(t.Meta.ReleaseDate())
	d.CanDownload = sess != nil && sess.User.CanDownload
	d.ParentID = m.LibraryID
	if al := t.Album; al != nil {
		d.ParentID, d.Album, d.AlbumID = al.ID, al.Name, al.ID
		if tag := images.ArtTag(al.Cover); tag != "" {
			d.AlbumPrimaryImageTag = tag
			d.ParentPrimaryImageItemID, d.ParentPrimaryImageTag = al.ID, tag
		}
	}
	d.AlbumArtists, _ = artistPairs(t.AlbumArtists)
	var names []string
	d.ArtistItems, names = artistPairs(t.Artists)
	d.Artists = names
	if len(t.AlbumArtists) > 0 {
		_, an := artistPairs(t.AlbumArtists)
		d.AlbumArtist = strings.Join(an, ", ")
	}
	if t.Number > 0 {
		d.IndexNumber = intPtr(t.Number)
	}
	d.ParentIndexNumber = intPtr(t.Disc)
	setGenres(&d, t.Meta.Genres)
	d.HasLyrics = t.Lyrics != "" || t.Meta.Lyrics
	d.NormalizationGain = t.Meta.TrackGain
	if id := t.Meta.MBTrackID; id != "" {
		d.ProviderIDs["MusicBrainzTrack"] = id
	}
	if id := t.Meta.MBAlbumID; id != "" {
		d.ProviderIDs["MusicBrainzAlbum"] = id
		d.ExternalUrls = append(d.ExternalUrls, musicBrainzURL("release", id))
	}
	if id := t.Meta.MBAlbumArtistID; id != "" {
		d.ProviderIDs["MusicBrainzAlbumArtist"] = id
	}
	if len(t.Meta.MBArtistIDs) > 0 {
		d.ProviderIDs["MusicBrainzArtist"] = t.Meta.MBArtistIDs[0]
	}
	if tag := images.ArtTag(trackCover(m)); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = 1
	}
	artistImages(&d, t.AlbumArtists)
	d.UserData = userDataDto(m.ID, user[m.ID], runtimeOf(m))
	if len(m.Versions) == 0 {
		return d
	}
	info := m.Versions[0].Info()
	d.Container = info.Container
	d.RunTimeTicks = ticks(m.Versions[0].Duration)
	showPath := sess != nil && sess.User.IsAdmin && fields.has("Path")
	if showPath {
		d.Path = m.Path
	}
	if fields.has("MediaSources") {
		src := sourceDto(m, m.Versions[0], 0, showPath)
		withGain(&src, m)
		d.MediaSources = []MediaSourceInfo{src}
	}
	if fields.has("MediaStreams") {
		d.MediaStreams = streamDtos(info)
	}
	return d
}

// withGain adds the track gain to the audio stream of a media source.
func withGain(src *MediaSourceInfo, m *catalog.Movie) {
	if m.Track == nil || m.Track.Meta.TrackGain == nil {
		return
	}
	src.VideoType = ""
	for i := range src.MediaStreams {
		if src.MediaStreams[i].Type == "Audio" {
			src.MediaStreams[i].NormalizationGain = m.Track.Meta.TrackGain
			break
		}
	}
}

func (s *Server) albumDto(al *catalog.Album, fields fieldSet, user map[string]store.UserData) BaseItemDto {
	d := s.audioBase(al.ID, al.Name, al.SortName, "MusicAlbum")
	d.IsFolder = true
	d.DateCreated = timePtr(al.DateCreated)
	d.ProductionYear = al.Year
	d.PremiereDate = timePtr(al.Premiere)
	d.ParentID = al.LibraryID
	d.RunTimeTicks = ticks(al.Duration)
	n := len(al.Tracks)
	d.ChildCount, d.RecursiveItemCount, d.SongCount = &n, &n, &n
	var names []string
	d.AlbumArtists, names = artistPairs(al.Artists)
	d.AlbumArtist = strings.Join(names, ", ")
	d.Artists = names
	d.ArtistItems = d.AlbumArtists
	setGenres(&d, al.Genres)
	if fields.has("Overview") {
		d.Overview = al.Overview
	}
	if al.MBID != "" {
		d.ProviderIDs["MusicBrainzAlbum"] = al.MBID
		d.ExternalUrls = append(d.ExternalUrls, musicBrainzURL("release", al.MBID))
	}
	if al.MBReleaseGroup != "" {
		d.ProviderIDs["MusicBrainzReleaseGroup"] = al.MBReleaseGroup
	}
	if len(al.Artists) > 0 && al.Artists[0].MBID != "" {
		d.ProviderIDs["MusicBrainzAlbumArtist"] = al.Artists[0].MBID
	}
	if tag := images.ArtTag(al.Cover); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = 1
	}
	artistImages(&d, al.Artists)
	d.UserData = groupUserData(al.ID, al.Tracks, user)
	return d
}

// groupUserData is the user's state of an album: played when all its
// tracks are, a favourite of its own.
func groupUserData(id string, tracks []*catalog.Movie, user map[string]store.UserData) UserItemDataDto {
	unplayed, plays := 0, 0
	var last time.Time
	for _, t := range tracks {
		ud := user[t.ID]
		if !ud.Played {
			unplayed++
		}
		plays += ud.PlayCount
		if ud.LastPlayed.After(last) {
			last = ud.LastPlayed
		}
	}
	own := user[id]
	u := UserItemDataDto{
		IsFavorite:        own.IsFavorite,
		Played:            len(tracks) > 0 && unplayed == 0,
		PlayCount:         plays,
		LastPlayedDate:    timePtr(last),
		UnplayedItemCount: &unplayed,
		Key:               id,
		ItemID:            id,
	}
	if played := len(tracks) - unplayed; played > 0 && len(tracks) > 0 {
		u.PlayedPercentage = 100 * float64(played) / float64(len(tracks))
	}
	return u
}

func (s *Server) artistDto(a *catalog.Artist, fields fieldSet, user map[string]store.UserData) BaseItemDto {
	d := s.audioBase(a.ID, a.Name, a.SortName, "MusicArtist")
	d.IsFolder = true
	d.DateCreated = timePtr(a.DateCreated)
	albums, songs := len(a.Albums), len(a.Tracks)
	children := albums
	if albums == 0 {
		children = len(a.Books)
	}
	d.ChildCount, d.AlbumCount, d.SongCount = &children, &albums, &songs
	d.RecursiveItemCount = &songs
	setGenres(&d, a.Genres)
	if fields.has("Overview") {
		d.Overview = a.Overview
	}
	if a.MBID != "" {
		d.ProviderIDs["MusicBrainzArtist"] = a.MBID
		d.ExternalUrls = append(d.ExternalUrls, musicBrainzURL("artist", a.MBID))
	}
	if tag := images.ArtTag(a.Image("Primary")); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = 1
	}
	if tag := images.ArtTag(a.Backdrop); tag != "" {
		d.BackdropImageTags = []string{tag}
	}
	if tag := images.ArtTag(a.Logo); tag != "" {
		d.ImageTags[string(images.Logo)] = tag
	}
	own := user[a.ID]
	d.UserData = UserItemDataDto{IsFavorite: own.IsFavorite, PlayCount: own.PlayCount,
		LastPlayedDate: timePtr(own.LastPlayed), Key: a.ID, ItemID: a.ID}
	return d
}

// narratorID gives narrators, who are not artists, a stable ID.
func narratorID(name string) string { return catalog.ArtistID("narrator:"+name, "") }

func (s *Server) bookDto(m *catalog.Movie, fields fieldSet, sess *Session, user map[string]store.UserData) BaseItemDto {
	b := m.Book
	d := s.audioBase(m.ID, m.Title, m.SortTitle, "AudioBook")
	d.MediaType = "Audio"
	d.DateCreated = timePtr(m.DateCreated)
	d.ProductionYear = m.Year
	d.PremiereDate = timePtr(b.Premiere)
	d.ParentID = m.LibraryID
	d.CanDownload = sess != nil && sess.User.CanDownload
	d.RunTimeTicks = ticks(b.Duration)
	var names []string
	d.AlbumArtists, names = artistPairs(b.Authors)
	d.ArtistItems, d.Artists = d.AlbumArtists, names
	d.AlbumArtist = strings.Join(names, ", ")
	d.Album = m.Title
	for _, a := range b.Authors {
		p := PersonDto{Name: a.Name, ID: a.ID, Type: "Author", Role: "Author"}
		if tag := images.ArtTag(a.Primary); tag != "" {
			p.PrimaryImageTag = tag
		}
		d.People = append(d.People, p)
	}
	for _, n := range b.Narrators {
		d.People = append(d.People, PersonDto{Name: n, ID: narratorID(n), Type: "Narrator", Role: "Narrator"})
	}
	if b.Series != "" {
		d.SeriesName = b.Series
		if n, err := strconv.Atoi(b.SeriesIndex); err == nil {
			d.IndexNumber = intPtr(n)
		}
	}
	setGenres(&d, b.Genres)
	d.Tags = append([]string{}, b.Tags...)
	if b.Publisher != "" {
		d.Studios = []NameIDPair{{Name: b.Publisher, ID: catalog.StudioID(b.Publisher)}}
	}
	if fields.has("Overview") {
		d.Overview = b.Overview
	}
	if tag := images.ArtTag(b.Cover); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = 1
	}
	d.UserData = userDataDto(m.ID, user[m.ID], b.Duration)
	if fields.has("Chapters") {
		d.Chapters = []ChapterInfoDto{}
		for _, c := range b.Chapters {
			d.Chapters = append(d.Chapters, ChapterInfoDto{StartPositionTicks: ticks(c.Start), Name: c.Name})
		}
	}
	if len(m.Versions) == 0 {
		return d
	}
	v := m.Versions[0]
	d.Container = v.Info().Container
	showPath := sess != nil && sess.User.IsAdmin && fields.has("Path")
	if showPath {
		d.Path = m.Path
	}
	if fields.has("MediaSources") {
		d.MediaSources = []MediaSourceInfo{s.bookSource(m, showPath)}
	}
	if fields.has("MediaStreams") {
		d.MediaStreams = streamDtos(v.Info())
	}
	return d
}

// bookSource describes an audiobook as one media source over the whole
// book, also when it is made of several files.
func (s *Server) bookSource(m *catalog.Movie, showPath bool) MediaSourceInfo {
	v := m.Versions[0]
	src := sourceDto(m, v, 0, showPath)
	src.VideoType = ""
	src.RunTimeTicks = ticks(m.Book.Duration)
	src.Size = v.Size
	if needsConversion(m) {
		// The stream endpoint serves single files; the whole book plays
		// only converted.
		src.SupportsDirectPlay, src.SupportsDirectStream = false, false
		src.SupportsTranscoding = true
	}
	return src
}

// musicGenreDto describes a genre of music or audiobooks.
func (s *Server) musicGenreDto(f *catalog.Facet, typ string) BaseItemDto {
	d := s.audioBase(f.ID, f.Name, strings.ToLower(f.Name), typ)
	d.IsFolder = true
	d.LocationType = "Virtual"
	n := f.Albums
	if n == 0 {
		n = f.Books
	}
	albums := f.Albums
	d.ChildCount, d.AlbumCount = &n, &albums
	d.UserData = UserItemDataDto{Key: f.ID, ItemID: f.ID}
	return d
}

// artists lists artists, or only album artists, of a library or all.
func (s *Server) artists(albumArtists bool) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *Session) {
		u, ok := s.targetUser(w, r, sess)
		if !ok {
			return
		}
		user, ok := s.userState(w, r, u)
		if !ok {
			return
		}
		snap := s.Catalog.Snapshot()
		start := intParam(r, "StartIndex", 0)
		q := catalog.AudioQuery{
			Types:                   map[string]bool{catalog.TypeArtist: true},
			Search:                  queryValue(r, "SearchTerm"),
			NameStartsWith:          queryValue(r, "NameStartsWith"),
			NameStartsWithOrGreater: queryValue(r, "NameStartsWithOrGreater"),
			NameLessThan:            queryValue(r, "NameLessThan"),
			SortBy:                  listParam(r, "SortBy"),
			StartIndex:              start,
			Limit:                   intParam(r, "Limit", 0),
			User:                    user,
		}
		if albumArtists {
			q.Types = map[string]bool{catalog.TypeAlbumArtist: true}
		}
		for _, o := range listParam(r, "SortOrder") {
			q.Descending = append(q.Descending, strings.EqualFold(o, "Descending"))
		}
		for _, id := range listParam(r, "GenreIds") {
			q.GenreIDs = append(q.GenreIDs, normalizeID(id))
		}
		var pq catalog.Query
		playStateFilters(r, &pq)
		q.Favorite = pq.Favorite
		if parent := normalizeID(queryValue(r, "ParentId")); parent != "" {
			if _, ok := snap.Library(parent); !ok {
				writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: []BaseItemDto{}, StartIndex: start})
				return
			}
			q.LibraryID = parent
		}
		fields := parseFields(queryValue(r, "Fields"))
		items, total := snap.AudioItems(q)
		if limitZero(r) {
			items = nil
		}
		out := QueryResult[BaseItemDto]{Items: make([]BaseItemDto, 0, len(items)), TotalRecordCount: total, StartIndex: start}
		for _, it := range items {
			out.Items = append(out.Items, s.artistDto(it.Artist, fields, user))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) artistByName(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	user, ok := s.userState(w, r, u)
	if !ok {
		return
	}
	a, ok := s.Catalog.Snapshot().ArtistByName(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.artistDto(a, nil, user))
}

// musicGenres lists the genres of music or audiobooks.
func (s *Server) musicGenres(typ string) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *Session) {
		search := queryValue(r, "SearchTerm")
		if search == "" {
			search = queryValue(r, "NameStartsWith")
		}
		var items []BaseItemDto
		for _, f := range s.Catalog.Snapshot().MusicGenres(normalizeID(queryValue(r, "ParentId")), search) {
			items = append(items, s.musicGenreDto(f, typ))
		}
		writeJSON(w, http.StatusOK, page(items, intParam(r, "StartIndex", 0), intParam(r, "Limit", 0)))
	}
}

func (s *Server) genreByName(typ string) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *Session) {
		snap := s.Catalog.Snapshot()
		name := r.PathValue("name")
		if f, ok := snap.MusicGenre(catalog.MusicGenreID(name)); ok && (typ == "MusicGenre" || !snap.HasGenre(name)) {
			writeJSON(w, http.StatusOK, s.musicGenreDto(f, typ))
			return
		}
		if f, ok := snap.Genre(catalog.GenreID(name)); ok {
			writeJSON(w, http.StatusOK, s.facetDto(f, "Genre", nil))
			return
		}
		http.NotFound(w, r)
	}
}

// wantsMusicGenres reports whether a /Genres request is about music or
// audiobooks.
func wantsMusicGenres(r *http.Request, snap *catalog.Snapshot) bool {
	if len(audioTypes(r)) > 0 {
		return true
	}
	l, ok := snap.Library(normalizeID(queryValue(r, "ParentId")))
	return ok && isAudioLibrary(l)
}

// audioStream serves a track or audiobook file as it is.
func (s *Server) audioStream(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.serveFile(w, r, sess, false)
}

// audioLatest lists the newest albums of a music library, or the newest
// books of an audiobook library, as Jellyfin groups the latest tracks
// into their albums.
func (s *Server) audioLatest(w http.ResponseWriter, r *http.Request, sess *Session, snap *catalog.Snapshot,
	user map[string]store.UserData, libraryID string, books bool) {
	q := catalog.AudioQuery{LibraryID: libraryID, Types: map[string]bool{catalog.TypeAlbum: true},
		SortBy: []string{"DateCreated"}, Descending: []bool{true}, Limit: intParam(r, "Limit", 20), User: user}
	if books || audioTypes(r)[catalog.TypeAudioBook] {
		q.Types = map[string]bool{catalog.TypeAudioBook: true}
	}
	if p := boolParam(r, "IsPlayed"); p != nil {
		q.Played = p
	}
	items, _ := snap.AudioItems(q)
	fields := parseFields(queryValue(r, "Fields"))
	out := []BaseItemDto{}
	for _, it := range items {
		out = append(out, s.audioItemDto(it, fields, sess, user))
	}
	writeJSON(w, http.StatusOK, out)
}

// similarMusic answers /Items/{id}/Similar for artists, albums and
// tracks; it reports false for anything else.
func (s *Server) similarMusic(w http.ResponseWriter, r *http.Request, sess *Session, snap *catalog.Snapshot, id string) bool {
	var artist *catalog.Artist
	var album *catalog.Album
	if a, ok := snap.Artist(id); ok {
		artist = a
	} else if al, ok := snap.Album(id); ok {
		album = al
	} else if m, ok := snap.Item(id); ok && m.Track != nil && m.Track.Album != nil {
		album = m.Track.Album
	} else {
		return false
	}
	user, ok := s.userState(w, r, sess.User)
	if !ok {
		return true
	}
	fields := parseFields(queryValue(r, "Fields"))
	limit := intParam(r, "Limit", 12)
	items := []BaseItemDto{}
	if artist != nil {
		for _, a := range snap.SimilarArtists(artist, limit) {
			items = append(items, s.artistDto(a, fields, user))
		}
	} else {
		for _, a := range snap.SimilarAlbums(album, limit) {
			items = append(items, s.albumDto(a, fields, user))
		}
	}
	writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: items, TotalRecordCount: len(items)})
	return true
}
