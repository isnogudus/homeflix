package jellyfin

import (
	"net/http"
	"strconv"
	"strings"

	"homeflix/internal/catalog"
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
	for _, p := range []string{"/Genres", "/Persons", "/Studios", "/MediaSegments/{itemId}"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.emptyItems))
	}
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
// does not have: other item types, play state, favourites, genres, people.
func noResults(r *http.Request) bool {
	if types := listParam(r, "IncludeItemTypes"); len(types) > 0 && !containsFold(types, "Movie", "Video") {
		return true
	}
	if containsFold(listParam(r, "ExcludeItemTypes"), "Movie") {
		return true
	}
	if containsFold(listParam(r, "Filters"), "IsFavorite", "IsPlayed", "IsResumable", "Likes", "IsFavoriteOrLikes", "IsFolder") {
		return true
	}
	if p := boolParam(r, "IsFavorite"); p != nil && *p {
		return true
	}
	if p := boolParam(r, "IsPlayed"); p != nil && *p {
		return true
	}
	for _, name := range []string{"Genres", "GenreIds", "PersonIds", "Person", "Studios", "StudioIds", "Tags",
		"OfficialRatings", "ArtistIds", "AlbumArtistIds"} {
		if len(listParam(r, name)) > 0 {
			return true
		}
	}
	return false
}

func (s *Server) getItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	if _, ok := s.targetUser(w, r, sess); !ok {
		return
	}
	snap := s.Catalog.Snapshot()
	fields := parseFields(queryValue(r, "Fields"))
	start, limit := intParam(r, "StartIndex", 0), intParam(r, "Limit", 0)
	empty := QueryResult[BaseItemDto]{Items: []BaseItemDto{}, StartIndex: start}

	if ids := listParam(r, "Ids"); len(ids) > 0 {
		var items []BaseItemDto
		for _, id := range ids {
			id = normalizeID(id)
			if m, ok := snap.Item(id); ok {
				items = append(items, s.itemDto(m, fields, sess))
			} else if l, ok := snap.Library(id); ok {
				items = append(items, s.viewDto(l))
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
	}
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
		out.Items = append(out.Items, s.itemDto(m, fields, sess))
	}
	writeJSON(w, http.StatusOK, out)
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
	if _, ok := s.targetUser(w, r, sess); !ok {
		return
	}
	snap := s.Catalog.Snapshot()
	id := normalizeID(r.PathValue("itemId"))
	if m, ok := snap.Item(id); ok {
		writeJSON(w, http.StatusOK, s.itemDto(m, nil, sess))
		return
	}
	if l, ok := snap.Library(id); ok {
		writeJSON(w, http.StatusOK, s.viewDto(l))
		return
	}
	http.NotFound(w, r)
}

func (s *Server) latestItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	if _, ok := s.targetUser(w, r, sess); !ok {
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
	fields := parseFields(queryValue(r, "Fields"))
	for _, m := range snap.Latest(parent, intParam(r, "Limit", 20)) {
		out = append(out, s.itemDto(m, fields, sess))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) itemFilters(w http.ResponseWriter, r *http.Request, sess *Session) {
	years := s.Catalog.Snapshot().Years(normalizeID(queryValue(r, "ParentId")))
	if years == nil {
		years = []int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Genres": []string{}, "Tags": []string{}, "OfficialRatings": []string{}, "Years": years,
	})
}

func (s *Server) itemFilters2(w http.ResponseWriter, r *http.Request, sess *Session) {
	writeJSON(w, http.StatusOK, map[string]any{"Genres": []any{}, "Tags": []string{}})
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
	m, ok := snap.Item(normalizeID(r.PathValue("itemId")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	out := []BaseItemDto{}
	if m.Parent != nil {
		out = append(out, s.itemDto(m.Parent, fieldSet{}, sess))
	}
	if l, ok := snap.Library(m.LibraryID); ok {
		out = append(out, s.viewDto(l))
	}
	writeJSON(w, http.StatusOK, out)
}

// similarItems has nothing to compare by until metadata exists.
func (s *Server) similarItems(w http.ResponseWriter, r *http.Request, sess *Session) {
	writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: []BaseItemDto{}})
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
	out := []BaseItemDto{}
	for _, x := range m.Extras {
		if (x.ExtraType == "Trailer") == trailers {
			out = append(out, s.itemDto(x, nil, sess))
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
