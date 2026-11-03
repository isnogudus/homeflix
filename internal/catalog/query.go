package catalog

import (
	"math/rand/v2"
	"sort"
	"strings"
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
	AudioLanguages          []string
	SubtitleLanguages       []string

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
			len(q.AudioLanguages) > 0 && !anyIn(q.AudioLanguages, m.audioLangs),
			len(q.SubtitleLanguages) > 0 && !anyIn(q.SubtitleLanguages, m.subtitleLangs):
			continue
		}
		out = append(out, m)
	}
	sortMovies(out, q.SortBy, q.Descending)
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

// sortMovies orders by the given Jellyfin sort fields, with the name as
// final tie-breaker. Unknown fields and fields without data (ratings, play
// state) are ignored.
func sortMovies(ms []*Movie, by []string, desc []bool) {
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
