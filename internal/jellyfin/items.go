package jellyfin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
)

func (s *Server) registerItems(rt *Router) {
	for _, p := range []string{"/UserViews", "/Users/{userId}/Views"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.userViews))
	}
	for _, p := range []string{"/Items", "/Users/{userId}/Items"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.getItems))
	}
	for _, p := range []string{"/Items/{itemId}", "/Users/{userId}/Items/{itemId}"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.getItem))
	}
	for _, p := range []string{"/Items/Latest", "/Users/{userId}/Items/Latest"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.latestItems))
	}
	for _, p := range []string{"/UserItems/Resume", "/Users/{userId}/Items/Resume"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.resumeItems))
	}
	rt.HandleFunc(http.MethodGet, "/Items/Filters", s.authed(s.itemFilters))
	rt.HandleFunc(http.MethodGet, "/Items/Filters2", s.authed(s.itemFilters2))
	rt.HandleFunc(http.MethodGet, "/Items/Counts", s.authed(s.itemCounts))
	rt.HandleFunc(http.MethodGet, "/Items/{itemId}/Ancestors", s.authed(s.ancestors))
	rt.HandleFunc(http.MethodGet, "/Items/{itemId}/Similar", s.authed(s.similarItems))
	rt.HandleFunc(http.MethodGet, "/Items/{itemId}/ThemeMedia", s.authed(s.themeMedia))
	for _, p := range []string{"/Items/{itemId}/SpecialFeatures", "/Users/{userId}/Items/{itemId}/SpecialFeatures"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.specialFeatures))
	}
	for _, p := range []string{"/Items/{itemId}/LocalTrailers", "/Users/{userId}/Items/{itemId}/LocalTrailers"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.localTrailers))
	}
	for _, p := range []string{"/Items/{itemId}/Intros", "/Users/{userId}/Items/{itemId}/Intros"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.emptyItems))
	}
	// homeflix has no series and no live TV.
	for _, p := range []string{"/MediaSegments/{itemId}", "/Shows/NextUp", "/LiveTv/Channels"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.emptyItems))
	}
	rt.HandleFunc(http.MethodGet, "/Genres", s.authed(s.facets("genre", "Genre")))
	rt.HandleFunc(http.MethodGet, "/Persons", s.authed(s.facets("person", "Person")))
	rt.HandleFunc(http.MethodGet, "/Studios", s.authed(s.facets("studio", "Studio")))
}

func (s *Server) emptyItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: []BaseItemDto{}})
}

// listParam splits a comma- (or pipe-) separated query parameter, also when
// it is repeated.
func listParam(r *http.Request, name string) []string {
	var out []string
	for k, vs := range r.URL.Query() {
		if !strings.EqualFold(k, name) {
			continue
		}
		for _, v := range vs {
			for _, p := range strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == '|' }) {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func intParam(r *http.Request, name string, def int) int {
	if v, err := strconv.Atoi(queryValue(r, name)); err == nil {
		return v
	}
	return def
}

func boolParam(r *http.Request, name string) *bool {
	switch strings.ToLower(queryValue(r, name)) {
	case "true", "1":
		t := true
		return &t
	case "false", "0":
		f := false
		return &f
	}
	return nil
}

func containsFold(list []string, want ...string) bool {
	for _, l := range list {
		for _, w := range want {
			if strings.EqualFold(l, w) {
				return true
			}
		}
	}
	return false
}

// userState loads the play state of the user a request is about. It
// writes an error response and returns false on failure.
func (s *Server) userState(w http.ResponseWriter, r *http.Request, u store.User) (map[string]store.UserData, bool) {
	ud, err := s.Store.AllUserData(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return ud, true
}

// userConfigBool reads a boolean from the user's client configuration.
func userConfigBool(u store.User, key string, def bool) bool {
	var cfg map[string]json.RawMessage
	if json.Unmarshal([]byte(u.Configuration), &cfg) != nil {
		return def
	}
	var v bool
	if raw, ok := cfg[key]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return def
}

func (s *Server) userViews(w http.ResponseWriter, r *http.Request, sess *Session) {
	if _, ok := s.targetUser(w, r, sess); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.viewsResult())
}

func (s *Server) viewsResult() QueryResult[BaseItemDto] {
	snap := s.Catalog.Snapshot()
	out := QueryResult[BaseItemDto]{Items: []BaseItemDto{}}
	for _, l := range snap.Libraries {
		out.Items = append(out.Items, s.viewDto(l))
	}
	out.TotalRecordCount = len(out.Items)
	return out
}

// noResults reports whether the request asks only for things homeflix
// does not have: other item types, folders, tags, music.
func noResults(r *http.Request) bool {
	if types := listParam(r, "IncludeItemTypes"); len(types) > 0 && !containsFold(types, "Movie", "Video") {
		return true
	}
	if containsFold(listParam(r, "ExcludeItemTypes"), "Movie") {
		return true
	}
	if containsFold(listParam(r, "Filters"), "IsFolder", "Dislikes") {
		return true
	}
	for _, name := range []string{"Person", "Tags", "OfficialRatings", "ArtistIds", "AlbumArtistIds"} {
		if len(listParam(r, name)) > 0 {
			return true
		}
	}
	return false
}

// facetFilters applies the genre, person and studio parameters.
func facetFilters(r *http.Request, q *catalog.Query) {
	for _, g := range listParam(r, "Genres") {
		q.GenreIDs = append(q.GenreIDs, catalog.GenreID(g))
	}
	for _, id := range listParam(r, "GenreIds") {
		q.GenreIDs = append(q.GenreIDs, normalizeID(id))
	}
	for _, id := range listParam(r, "PersonIds") {
		q.PersonIDs = append(q.PersonIDs, normalizeID(id))
	}
	for _, st := range listParam(r, "Studios") {
		q.StudioIDs = append(q.StudioIDs, catalog.StudioID(st))
	}
	for _, id := range listParam(r, "StudioIds") {
		q.StudioIDs = append(q.StudioIDs, normalizeID(id))
	}
}

func truePtr() *bool  { t := true; return &t }
func falsePtr() *bool { f := false; return &f }

// playStateFilters applies the Filters, IsPlayed and IsFavorite parameters.
func playStateFilters(r *http.Request, q *catalog.Query) {
	for _, f := range listParam(r, "Filters") {
		switch strings.ToLower(f) {
		case "isplayed":
			q.Played = truePtr()
		case "isunplayed":
			q.Played = falsePtr()
		case "isfavorite", "likes", "isfavoriteorlikes":
			q.Favorite = truePtr()
		case "isresumable":
			q.Resumable = truePtr()
		}
	}
	if p := boolParam(r, "IsPlayed"); p != nil {
		q.Played = p
	}
	if p := boolParam(r, "IsFavorite"); p != nil {
		q.Favorite = p
	}
}

func (s *Server) getItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	user, ok := s.userState(w, r, u)
	if !ok {
		return
	}
	snap := s.Catalog.Snapshot()
	fields := parseFields(queryValue(r, "Fields"))
	start, limit := intParam(r, "StartIndex", 0), intParam(r, "Limit", 0)
	empty := QueryResult[BaseItemDto]{Items: []BaseItemDto{}, StartIndex: start}

	if ids := listParam(r, "Ids"); len(ids) > 0 {
		var items []BaseItemDto
		for _, id := range ids {
			if d, ok := s.anyItemDto(snap, normalizeID(id), fields, sess, user); ok {
				items = append(items, d)
			}
		}
		writeJSON(w, http.StatusOK, page(items, start, limit))
		return
	}

	parent := normalizeID(queryValue(r, "ParentId"))
	recursive := boolParam(r, "Recursive")
	search := queryValue(r, "SearchTerm")
	if parent == "" && (recursive == nil || !*recursive) && search == "" {
		writeJSON(w, http.StatusOK, s.viewsResult())
		return
	}
	q := catalog.Query{
		Search:                  search,
		NameStartsWith:          queryValue(r, "NameStartsWith"),
		NameStartsWithOrGreater: queryValue(r, "NameStartsWithOrGreater"),
		NameLessThan:            queryValue(r, "NameLessThan"),
		HasSubtitles:            boolParam(r, "HasSubtitles"),
		IsHD:                    boolParam(r, "IsHd"),
		Is4K:                    boolParam(r, "Is4K"),
		HasSpecialFeature:       boolParam(r, "HasSpecialFeature"),
		HasTrailer:              boolParam(r, "HasTrailer"),
		AudioLanguages:          listParam(r, "AudioLanguages"),
		SubtitleLanguages:       listParam(r, "SubtitleLanguages"),
		SortBy:                  listParam(r, "SortBy"),
		StartIndex:              start,
		Limit:                   limit,
		User:                    user,
	}
	playStateFilters(r, &q)
	facetFilters(r, &q)
	for _, o := range listParam(r, "SortOrder") {
		q.Descending = append(q.Descending, strings.EqualFold(o, "Descending"))
	}
	for _, y := range listParam(r, "Years") {
		if n, err := strconv.Atoi(y); err == nil {
			q.Years = append(q.Years, n)
		}
	}
	for _, p := range listParam(r, "AnyProviderIdEquals") {
		if k, v, ok := strings.Cut(p, "."); ok && strings.EqualFold(k, "imdb") {
			q.IMDbID = v
		}
	}
	if parent != "" {
		if _, ok := snap.Library(parent); !ok {
			writeJSON(w, http.StatusOK, empty)
			return
		}
		q.LibraryID = parent
	}
	if noResults(r) {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	movies, total := snap.Movies(q)
	out := QueryResult[BaseItemDto]{Items: make([]BaseItemDto, 0, len(movies)), TotalRecordCount: total, StartIndex: start}
	for _, m := range movies {
		out.Items = append(out.Items, s.itemDto(m, fields, sess, user))
	}
	writeJSON(w, http.StatusOK, out)
}

// anyItemDto describes a movie, extra, part or library by ID.
func (s *Server) anyItemDto(snap *catalog.Snapshot, id string, fields fieldSet, sess *Session,
	user map[string]store.UserData) (BaseItemDto, bool) {
	if m, ok := snap.Item(id); ok {
		return s.itemDto(m, fields, sess, user), true
	}
	if p, ok := snap.Part(id); ok && p.Index > 0 {
		return s.partDto(p, fields, sess, user), true
	}
	if l, ok := snap.Library(id); ok {
		return s.viewDto(l), true
	}
	if f, ok := snap.Genre(id); ok {
		return s.facetDto(f, "Genre", fields), true
	}
	if f, ok := snap.Person(id); ok {
		return s.facetDto(f, "Person", fields), true
	}
	if f, ok := snap.Studio(id); ok {
		return s.facetDto(f, "Studio", fields), true
	}
	return BaseItemDto{}, false
}

// facets lists genres, people or studios, optionally of one library and
// matching SearchTerm or NameStartsWith.
func (s *Server) facets(kind, typ string) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *Session) {
		search := queryValue(r, "SearchTerm")
		if search == "" {
			search = queryValue(r, "NameStartsWith")
		}
		fields := parseFields(queryValue(r, "Fields"))
		var items []BaseItemDto
		for _, f := range s.Catalog.Snapshot().Facets(kind, normalizeID(queryValue(r, "ParentId")), search) {
			items = append(items, s.facetDto(f, typ, fields))
		}
		writeJSON(w, http.StatusOK, page(items, intParam(r, "StartIndex", 0), intParam(r, "Limit", 0)))
	}
}

func page(items []BaseItemDto, start, limit int) QueryResult[BaseItemDto] {
	total := len(items)
	start = min(max(start, 0), total)
	items = items[start:]
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []BaseItemDto{}
	}
	return QueryResult[BaseItemDto]{Items: items, TotalRecordCount: total, StartIndex: start}
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	user, ok := s.userState(w, r, u)
	if !ok {
		return
	}
	d, ok := s.anyItemDto(s.Catalog.Snapshot(), normalizeID(r.PathValue("itemId")), nil, sess, user)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) latestItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	user, ok := s.userState(w, r, u)
	if !ok {
		return
	}
	snap := s.Catalog.Snapshot()
	out := []BaseItemDto{}
	parent := normalizeID(queryValue(r, "ParentId"))
	if parent != "" {
		if _, ok := snap.Library(parent); !ok {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	if noResults(r) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	q := catalog.Query{LibraryID: parent, SortBy: []string{"DateCreated"}, Descending: []bool{true},
		Limit: intParam(r, "Limit", 20), User: user}
	if userConfigBool(u, "HidePlayedInLatest", true) {
		q.Played = falsePtr()
	}
	if p := boolParam(r, "IsPlayed"); p != nil {
		q.Played = p
	}
	movies, _ := snap.Movies(q)
	fields := parseFields(queryValue(r, "Fields"))
	for _, m := range movies {
		out = append(out, s.itemDto(m, fields, sess, user))
	}
	writeJSON(w, http.StatusOK, out)
}

// resumeItems lists movies and parts the user started and did not finish.
func (s *Server) resumeItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	user, ok := s.userState(w, r, u)
	if !ok {
		return
	}
	start, limit := intParam(r, "StartIndex", 0), intParam(r, "Limit", 0)
	if noResults(r) {
		writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: []BaseItemDto{}, StartIndex: start})
		return
	}
	snap := s.Catalog.Snapshot()
	fields := parseFields(queryValue(r, "Fields"))
	var items []BaseItemDto
	for _, res := range snap.Resume(user, normalizeID(queryValue(r, "ParentId")), 0) {
		if res.Index == 0 {
			items = append(items, s.itemDto(res.Movie, fields, sess, user))
		} else {
			items = append(items, s.partDto(res.PartRef, fields, sess, user))
		}
	}
	writeJSON(w, http.StatusOK, page(items, start, limit))
}

func (s *Server) itemFilters(w http.ResponseWriter, r *http.Request, sess *Session) {
	snap := s.Catalog.Snapshot()
	lib := normalizeID(queryValue(r, "ParentId"))
	years := snap.Years(lib)
	if years == nil {
		years = []int{}
	}
	genres := []string{}
	for _, f := range snap.Facets("genre", lib, "") {
		genres = append(genres, f.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Genres": genres, "Tags": []string{}, "OfficialRatings": snap.Certifications(lib), "Years": years,
	})
}

func (s *Server) itemFilters2(w http.ResponseWriter, r *http.Request, sess *Session) {
	genres := []NameIDPair{}
	for _, f := range s.Catalog.Snapshot().Facets("genre", normalizeID(queryValue(r, "ParentId")), "") {
		genres = append(genres, NameIDPair{Name: f.Name, ID: f.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"Genres": genres, "Tags": []string{}})
}

func (s *Server) itemCounts(w http.ResponseWriter, r *http.Request, sess *Session) {
	snap := s.Catalog.Snapshot()
	n := len(snap.AllMovies())
	counts := map[string]int{"MovieCount": n, "ItemCount": n}
	for _, k := range []string{"SeriesCount", "EpisodeCount", "ArtistCount", "ProgramCount", "TrailerCount",
		"SongCount", "AlbumCount", "MusicVideoCount", "BoxSetCount", "BookCount"} {
		counts[k] = 0
	}
	writeJSON(w, http.StatusOK, counts)
}

func (s *Server) ancestors(w http.ResponseWriter, r *http.Request, sess *Session) {
	snap := s.Catalog.Snapshot()
	id := normalizeID(r.PathValue("itemId"))
	m, ok := snap.Item(id)
	if !ok {
		p, isPart := snap.Part(id)
		if !isPart {
			http.NotFound(w, r)
			return
		}
		m = p.Movie
	}
	out := []BaseItemDto{}
	if m.Parent != nil {
		out = append(out, s.itemDto(m.Parent, fieldSet{}, sess, nil))
	}
	if l, ok := snap.Library(m.LibraryID); ok {
		out = append(out, s.viewDto(l))
	}
	writeJSON(w, http.StatusOK, out)
}

// similarItems lists movies sharing genres with the item.
func (s *Server) similarItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	snap := s.Catalog.Snapshot()
	m, ok := snap.Item(normalizeID(r.PathValue("itemId")))
	if !ok {
		writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: []BaseItemDto{}})
		return
	}
	user, ok := s.userState(w, r, sess.User)
	if !ok {
		return
	}
	fields := parseFields(queryValue(r, "Fields"))
	items := []BaseItemDto{}
	for _, o := range snap.Similar(m, intParam(r, "Limit", 12)) {
		items = append(items, s.itemDto(o, fields, sess, user))
	}
	writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: items, TotalRecordCount: len(items)})
}

func (s *Server) themeMedia(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := normalizeID(r.PathValue("itemId"))
	empty := map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0, "OwnerId": id}
	writeJSON(w, http.StatusOK, map[string]any{
		"ThemeVideosResult": empty, "ThemeSongsResult": empty, "SoundtrackSongsResult": empty,
	})
}

func (s *Server) extras(w http.ResponseWriter, r *http.Request, sess *Session, trailers bool) {
	m, ok := s.Catalog.Snapshot().Item(normalizeID(r.PathValue("itemId")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess.User)
	if !ok {
		return
	}
	out := []BaseItemDto{}
	for _, x := range m.Extras {
		if (x.ExtraType == "Trailer") == trailers {
			out = append(out, s.itemDto(x, nil, sess, user))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) specialFeatures(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.extras(w, r, sess, false)
}

func (s *Server) localTrailers(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.extras(w, r, sess, true)
}
