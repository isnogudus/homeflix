package catalog

import (
	"math/rand/v2"
	"sort"
	"strings"

	"homeflix/internal/store"
)

// Query selects, orders and pages movies.
type Query struct {
	LibraryID string   // "" for all libraries
	IDs       []string // restrict to these movies
	Search    string

	NameStartsWith          string
	NameStartsWithOrGreater string
	NameLessThan            string
	Years                   []int
	IMDbID                  string
	HasSubtitles, IsHD      *bool
	Is4K                    *bool
	HasSpecialFeature       *bool
	HasTrailer              *bool

	// User is the requesting user's state by item ID, for the play state
	// filters and sorts.
	User              map[string]store.UserData
	Played            *bool
	Favorite          *bool
	Resumable         *bool
	AudioLanguages    []string
	SubtitleLanguages []string

	SortBy     []string // Jellyfin names: SortName, ProductionYear, DateCreated, Runtime, Random, …
	Descending []bool   // per SortBy entry; missing entries take the first, else ascending
	StartIndex int
	Limit      int // 0 for no limit
}

// Movies returns the requested page and the number of matching movies.
func (s *Snapshot) Movies(q Query) ([]*Movie, int) {
	ids := map[string]bool{}
	for _, id := range q.IDs {
		ids[id] = true
	}
	years := map[int]bool{}
	for _, y := range q.Years {
		years[y] = true
	}
	search := SearchKey(q.Search)
	starts := SearchKey(q.NameStartsWith)
	orGreater := SearchKey(q.NameStartsWithOrGreater)
	lessThan := SearchKey(q.NameLessThan)

	var out []*Movie
	for _, m := range s.movies {
		switch {
		case q.LibraryID != "" && m.LibraryID != q.LibraryID,
			len(ids) > 0 && !ids[m.ID],
			search != "" && !strings.Contains(m.searchKey, search),
			starts != "" && !strings.HasPrefix(m.SortTitle, starts),
			orGreater != "" && m.SortTitle < orGreater,
			lessThan != "" && m.SortTitle >= lessThan,
			len(years) > 0 && !years[m.Year],
			q.IMDbID != "" && !strings.EqualFold(m.IMDbID, q.IMDbID),
			q.HasSubtitles != nil && m.hasSubtitles != *q.HasSubtitles,
			q.IsHD != nil && (m.maxHeight >= 700) != *q.IsHD,
			q.Is4K != nil && (m.maxHeight >= 2000) != *q.Is4K,
			q.HasSpecialFeature != nil && m.hasExtra(false) != *q.HasSpecialFeature,
			q.HasTrailer != nil && m.hasExtra(true) != *q.HasTrailer,
			q.Played != nil && q.User[m.ID].Played != *q.Played,
			q.Favorite != nil && q.User[m.ID].IsFavorite != *q.Favorite,
			q.Resumable != nil && (q.User[m.ID].Position > 0) != *q.Resumable,
			len(q.AudioLanguages) > 0 && !anyIn(q.AudioLanguages, m.audioLangs),
			len(q.SubtitleLanguages) > 0 && !anyIn(q.SubtitleLanguages, m.subtitleLangs):
			continue
		}
		out = append(out, m)
	}
	sortMovies(out, q.SortBy, q.Descending, q.User)
	total := len(out)
	start := min(max(q.StartIndex, 0), total)
	out = out[start:]
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, total
}

// hasExtra reports whether the movie has a trailer (trailer true) or
// another extra.
func (m *Movie) hasExtra(trailer bool) bool {
	for _, x := range m.Extras {
		if (x.ExtraType == "Trailer") == trailer {
			return true
		}
	}
	return false
}

func anyIn(want []string, have map[string]bool) bool {
	for _, w := range want {
		if have[strings.ToLower(w)] {
			return true
		}
	}
	return false
}

func runtime(m *Movie) int64 {
	if len(m.Versions) == 0 {
		return 0
	}
	return int64(m.Versions[0].Duration)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// sortMovies orders by the given Jellyfin sort fields, with the name as
// final tie-breaker. Unknown fields and fields without data (ratings) are
// ignored.
func sortMovies(ms []*Movie, by []string, desc []bool, user map[string]store.UserData) {
	type key struct {
		cmp  func(a, b *Movie) int
		desc bool
	}
	var keys []key
	for i, field := range by {
		d := false
		switch {
		case i < len(desc):
			d = desc[i]
		case len(desc) > 0:
			d = desc[0]
		}
		var cmp func(a, b *Movie) int
		switch strings.ToLower(field) {
		case "sortname", "name":
			cmp = func(a, b *Movie) int { return strings.Compare(a.SortTitle, b.SortTitle) }
		case "productionyear", "premieredate":
			cmp = func(a, b *Movie) int { return a.Year - b.Year }
		case "datecreated":
			cmp = func(a, b *Movie) int { return a.DateCreated.Compare(b.DateCreated) }
		case "runtime":
			cmp = func(a, b *Movie) int { return compareInt(runtime(a), runtime(b)) }
		case "dateplayed":
			cmp = func(a, b *Movie) int { return user[a.ID].LastPlayed.Compare(user[b.ID].LastPlayed) }
		case "playcount":
			cmp = func(a, b *Movie) int { return user[a.ID].PlayCount - user[b.ID].PlayCount }
		case "isfavoriteorliked":
			cmp = func(a, b *Movie) int { return compareBool(user[a.ID].IsFavorite, user[b.ID].IsFavorite) }
		case "isplayed":
			cmp = func(a, b *Movie) int { return compareBool(user[a.ID].Played, user[b.ID].Played) }
		case "isunplayed":
			cmp = func(a, b *Movie) int { return compareBool(!user[a.ID].Played, !user[b.ID].Played) }
		case "random":
			rand.Shuffle(len(ms), func(i, j int) { ms[i], ms[j] = ms[j], ms[i] })
			return
		default:
			continue
		}
		keys = append(keys, key{cmp, d})
	}
	sort.SliceStable(ms, func(i, j int) bool {
		for _, k := range keys {
			c := k.cmp(ms[i], ms[j])
			if k.desc {
				c = -c
			}
			if c != 0 {
				return c < 0
			}
		}
		return lessByName(ms[i], ms[j])
	})
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Resumable is something the user started and did not finish: a movie, or
// a later part of a split movie.
type Resumable struct {
	PartRef
	Data store.UserData
}

// Resume returns what the user can continue, most recently played first.
func (s *Snapshot) Resume(user map[string]store.UserData, libraryID string, limit int) []Resumable {
	var out []Resumable
	for id, d := range user {
		if d.Position <= 0 || d.Played {
			continue
		}
		var r PartRef
		if m, ok := s.byID[id]; ok && !m.IsExtra() && len(m.Versions) > 0 {
			r = PartRef{Movie: m, Version: m.Versions[0]}
		} else if p, ok := s.parts[id]; ok && p.Index > 0 && !p.Movie.IsExtra() {
			r = p
		} else {
			continue
		}
		if libraryID != "" && r.Movie.LibraryID != libraryID {
			continue
		}
		out = append(out, Resumable{PartRef: r, Data: d})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Data.LastPlayed.Equal(out[j].Data.LastPlayed) {
			return out[i].Data.LastPlayed.After(out[j].Data.LastPlayed)
		}
		return out[i].Data.ItemID < out[j].Data.ItemID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Latest returns the most recently added movies of a library ("" for all).
func (s *Snapshot) Latest(libraryID string, limit int) []*Movie {
	ms, _ := s.Movies(Query{LibraryID: libraryID, SortBy: []string{"DateCreated"}, Descending: []bool{true}, Limit: limit})
	return ms
}

// Years lists the production years present in a library, ascending.
func (s *Snapshot) Years(libraryID string) []int {
	seen := map[int]bool{}
	var out []int
	for _, m := range s.movies {
		if (libraryID == "" || m.LibraryID == libraryID) && m.Year > 0 && !seen[m.Year] {
			seen[m.Year] = true
			out = append(out, m.Year)
		}
	}
	sort.Ints(out)
	return out
}
