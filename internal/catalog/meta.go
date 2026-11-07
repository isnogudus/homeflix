package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/library"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

// Meta is TMDB's description of a movie.
type Meta struct {
	tmdb.Movie
	// PosterFile and BackdropFile are local artwork, "" if there is none.
	PosterFile, BackdropFile string
}

// Premiere returns the release date, or the zero time.
func (m *Meta) Premiere() time.Time { return parseDate(m.ReleaseDate) }

// Name is the title clients show: TMDB's if the movie was matched.
func (m *Movie) Name() string {
	if m.Meta != nil && m.Meta.Title != "" {
		return m.Meta.Title
	}
	return m.Title
}

// Facet is a genre, person or studio movies can be filtered by.
type Facet struct {
	ID     string
	Name   string
	TMDbID int // people only
	Movies int
	// Albums and Books count the albums and audiobooks of a music or
	// audiobook genre.
	Albums, Books int
	libs          map[string]bool // libraries of a music or audiobook genre
	// Person is what TMDB knows about a person, nil if nothing yet.
	Person *PersonInfo
}

// PersonInfo describes a member of cast or crew.
type PersonInfo struct {
	tmdb.PersonDetails
	// PhotoFile is the local portrait, "" if there is none.
	PhotoFile string
}

// Born and Died return the dates of birth and death, or the zero time.
func (p *PersonInfo) Born() time.Time { return parseDate(p.Birthday) }
func (p *PersonInfo) Died() time.Time { return parseDate(p.Deathday) }

func parseDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (f *Facet) applyPerson(p store.Person, opt Options) {
	if p.Status != store.MetaMatched || p.Data == "" {
		return
	}
	info := &PersonInfo{}
	if err := json.Unmarshal([]byte(p.Data), &info.PersonDetails); err != nil {
		if opt.Log != nil {
			opt.Log.Warn("unreadable person", "name", f.Name, "err", err)
		}
		return
	}
	if p.Photo != "" && opt.MetadataDir != "" {
		path := filepath.Join(opt.MetadataDir, p.Photo)
		if _, err := os.Stat(path); err == nil {
			info.PhotoFile = path
		}
	}
	f.Person = info
}

// facetID derives a stable 32-digit ID from a kind and a key.
func facetID(kind, key string) string {
	sum := sha256.Sum256([]byte(kind + ":" + strings.ToLower(key)))
	return hex.EncodeToString(sum[:16])
}

// GenreID, PersonID and StudioID return the IDs clients address them by.
func GenreID(name string) string  { return facetID("genre", name) }
func PersonID(tmdbID int) string  { return facetID("person", strconv.Itoa(tmdbID)) }
func StudioID(name string) string { return facetID("studio", name) }

// MusicGenreID is the ID of a genre of music or audiobooks; genres of
// movies of the same name are other items.
func MusicGenreID(name string) string { return facetID("musicgenre", name) }

func (m *Movie) applyMeta(md store.Metadata, opt Options) {
	if md.Status != store.MetaMatched || md.Data == "" {
		return
	}
	var tm tmdb.Movie
	if err := json.Unmarshal([]byte(md.Data), &tm); err != nil {
		if opt.Log != nil {
			opt.Log.Warn("unreadable metadata", "item", m.Title, "err", err)
		}
		return
	}
	if tmdb.NotRated(tm.Certification) {
		tm.Certification = ""
	}
	meta := &Meta{Movie: tm}
	for _, f := range []struct {
		name string
		dst  *string
	}{{md.Poster, &meta.PosterFile}, {md.Backdrop, &meta.BackdropFile}} {
		if f.name == "" || opt.MetadataDir == "" {
			continue
		}
		path := filepath.Join(opt.MetadataDir, f.name)
		if _, err := os.Stat(path); err == nil {
			*f.dst = path
		}
	}
	m.Meta = meta
	if tm.Title != "" {
		m.SortTitle = library.SortKey(tm.Title, opt.Articles)
	}
	if m.Year == 0 {
		m.Year = meta.Premiere().Year()
	}
	if m.IMDbID == "" {
		m.IMDbID = tm.IMDbID
	}
	if m.TMDbID == "" && tm.ID > 0 {
		m.TMDbID = strconv.Itoa(tm.ID)
	}
}

// index records the genres, people and studios of a movie.
func (s *Snapshot) index(m *Movie) {
	m.genreIDs, m.personIDs, m.studioIDs = map[string]bool{}, map[string]bool{}, map[string]bool{}
	if m.Meta == nil || m.ParentID != "" {
		return
	}
	add := func(facets map[string]*Facet, ids map[string]bool, id, name string, tmdbID int) {
		if ids[id] {
			return
		}
		ids[id] = true
		f, ok := facets[id]
		if !ok {
			f = &Facet{ID: id, Name: name, TMDbID: tmdbID}
			facets[id] = f
		}
		f.Movies++
	}
	for _, g := range m.Meta.Genres {
		add(s.genres, m.genreIDs, GenreID(g), g, 0)
	}
	for _, p := range m.Meta.People {
		add(s.people, m.personIDs, PersonID(p.ID), p.Name, p.ID)
	}
	for _, st := range m.Meta.Studios {
		add(s.studios, m.studioIDs, StudioID(st), st, 0)
	}
}

// Genre, Person and Studio look a facet up by ID.
func (s *Snapshot) Genre(id string) (*Facet, bool)  { f, ok := s.genres[id]; return f, ok }
func (s *Snapshot) Person(id string) (*Facet, bool) { f, ok := s.people[id]; return f, ok }
func (s *Snapshot) Studio(id string) (*Facet, bool) { f, ok := s.studios[id]; return f, ok }

// Facets lists the genres ("genre"), people ("person") or studios
// ("studio") of a library ("" for all) whose name contains search, by name.
func (s *Snapshot) Facets(kind, libraryID, search string) []*Facet {
	var src map[string]*Facet
	var has func(*Movie, string) bool
	switch kind {
	case "genre":
		src, has = s.genres, func(m *Movie, id string) bool { return m.genreIDs[id] }
	case "person":
		src, has = s.people, func(m *Movie, id string) bool { return m.personIDs[id] }
	case "studio":
		src, has = s.studios, func(m *Movie, id string) bool { return m.studioIDs[id] }
	default:
		return nil
	}
	key := SearchKey(search)
	var out []*Facet
	for id, f := range src {
		if key != "" && !strings.Contains(SearchKey(f.Name), key) {
			continue
		}
		if libraryID != "" {
			in := false
			for _, m := range s.movies {
				if m.LibraryID == libraryID && has(m, id) {
					in = true
					break
				}
			}
			if !in {
				continue
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Similar returns movies sharing the most genres with m, then the closest
// in time.
func (s *Snapshot) Similar(m *Movie, limit int) []*Movie {
	if len(m.genreIDs) == 0 {
		return nil
	}
	type scored struct {
		m      *Movie
		shared int
		gap    int
	}
	var cands []scored
	for _, o := range s.movies {
		if o == m {
			continue
		}
		n := 0
		for id := range m.genreIDs {
			if o.genreIDs[id] {
				n++
			}
		}
		if n == 0 {
			continue
		}
		gap := m.Year - o.Year
		if gap < 0 {
			gap = -gap
		}
		cands = append(cands, scored{o, n, gap})
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.shared != b.shared {
			return a.shared > b.shared
		}
		if a.gap != b.gap {
			return a.gap < b.gap
		}
		return lessByName(a.m, b.m)
	})
	out := make([]*Movie, 0, min(limit, len(cands)))
	for _, c := range cands {
		if len(out) == limit {
			break
		}
		out = append(out, c.m)
	}
	return out
}

// Certifications lists the age ratings present in a library, sorted.
func (s *Snapshot) Certifications(libraryID string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range s.movies {
		if m.Meta == nil || m.Meta.Certification == "" || (libraryID != "" && m.LibraryID != libraryID) {
			continue
		}
		if !seen[m.Meta.Certification] {
			seen[m.Meta.Certification] = true
			out = append(out, m.Meta.Certification)
		}
	}
	sort.Strings(out)
	return out
}
