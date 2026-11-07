// Package catalog keeps an in-memory view of the library for answering
// client queries: movies with their versions, parts, extras and parsed
// stream information. It is rebuilt from the database after scans.
package catalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"homeflix/internal/library"
	"homeflix/internal/media"
	"homeflix/internal/store"
)

// Library is a library view with its movie count.
type Library struct {
	store.Library
	Movies int
}

// Part is one file of a version.
type Part struct {
	File store.MediaFile // probe JSON cleared after parsing
	Info media.Info
}

// Version is one way to play a movie, made of one or more parts. Its ID is
// the media source ID clients use: the movie's own ID for the first
// version, the first part's file ID for the others.
type Version struct {
	ID       string
	Label    string
	Parts    []Part
	Size     int64
	Duration time.Duration
}

// Info returns the stream information of the first part.
func (v *Version) Info() media.Info { return v.Parts[0].Info }

// Movie is a playable movie or extra.
type Movie struct {
	store.Item
	Versions []*Version // best first
	Extras   []*Movie
	Parent   *Movie // set for extras

	// Meta is what TMDB knows about the movie; nil if nothing.
	Meta *Meta
	// Collection is the film series the movie belongs to, nil if none.
	Collection *Collection
	// Set is non-nil when this is not a movie but the list entry of a
	// collection (Type "BoxSet").
	Set *Collection
	// Track is set for a track of a music library (Type "Audio"), Book
	// for an audiobook (Type "AudioBook").
	Track *Track
	Book  *Book

	searchKey     string
	genreIDs      map[string]bool
	personIDs     map[string]bool
	studioIDs     map[string]bool
	maxHeight     int
	hasSubtitles  bool
	audioLangs    map[string]bool
	subtitleLangs map[string]bool
}

// IsExtra reports whether the movie is an extra of another movie.
func (m *Movie) IsExtra() bool { return m.Parent != nil }

// PartCount is the largest number of parts of any version. Clients ask
// for additional parts only when the movie reports more than one, also for
// an alternate version that is split while the default one is not.
func (m *Movie) PartCount() int {
	n := 0
	for _, v := range m.Versions {
		n = max(n, len(v.Parts))
	}
	return n
}

// PartRef locates one file of a movie. Files after the first of a version
// are addressed by their file ID, as Jellyfin's additional parts.
type PartRef struct {
	Movie   *Movie
	Version *Version
	Index   int // 0 for the first part
}

// Part returns the part.
func (r PartRef) Part() Part { return r.Version.Parts[r.Index] }

// IsLast reports whether this is the last part of its version.
func (r PartRef) IsLast() bool { return r.Index == len(r.Version.Parts)-1 }

// Snapshot is an immutable state of the catalog.
type Snapshot struct {
	Libraries []Library
	movies    []*Movie // present top-level movies, by sort title
	byID      map[string]*Movie
	sources   map[string]*Version
	sourceOf  map[*Version]*Movie
	parts     map[string]PartRef // by file ID
	genres    map[string]*Facet
	people    map[string]*Facet
	studios   map[string]*Facet
	// collections by ID, and by name
	collections    map[string]*Collection
	collectionList []*Collection
	locale         media.Locale
	music          *musicIndex
	BuiltAt        time.Time
}

// Catalog holds the current snapshot.
type Catalog struct {
	Store    *store.Store
	Locale   media.Locale
	Articles []string // leading words ignored when sorting TMDB titles
	// MetadataDir holds artwork fetched from TMDB.
	MetadataDir string
	Log         *slog.Logger

	snap atomic.Pointer[Snapshot]
}

// Snapshot returns the current state; it is never nil after the first
// Reload, and an empty snapshot before.
func (c *Catalog) Snapshot() *Snapshot {
	if s := c.snap.Load(); s != nil {
		return s
	}
	return emptySnapshot
}

var emptySnapshot = Build(nil, nil, Options{})

// Reload rebuilds the snapshot from the database.
func (c *Catalog) Reload(ctx context.Context) error {
	start := time.Now()
	libs, err := c.Store.Libraries(ctx)
	if err != nil {
		return err
	}
	items, err := c.Store.PresentItems(ctx)
	if err != nil {
		return err
	}
	meta, err := c.Store.AllMetadata(ctx)
	if err != nil {
		return err
	}
	people, err := c.Store.AllPeople(ctx)
	if err != nil {
		return err
	}
	cols, err := c.Store.AllCollections(ctx)
	if err != nil {
		return err
	}
	sidecars, err := c.Store.Sidecars(ctx, "")
	if err != nil {
		return err
	}
	snap := Build(libs, items, Options{Locale: c.Locale, Articles: c.Articles, Metadata: meta, People: people,
		Collections: cols, Sidecars: sidecars, MetadataDir: c.MetadataDir, Log: c.Log})
	c.Set(snap)
	if c.Log != nil {
		attrs := []any{"movies", len(snap.movies)}
		if snap.HasAudio() {
			attrs = append(attrs, "tracks", len(snap.music.tracks), "albums", len(snap.music.albums),
				"artists", len(snap.music.artists), "books", len(snap.music.books))
		}
		c.Log.Info("catalog loaded", append(attrs, "took", time.Since(start).Round(time.Millisecond))...)
	}
	return nil
}

// Set replaces the current snapshot.
func (c *Catalog) Set(s *Snapshot) {
	s.BuiltAt = time.Now()
	c.snap.Store(s)
}

// Options control how a snapshot is built.
type Options struct {
	Locale      media.Locale
	Articles    []string
	Metadata    map[string]store.Metadata // by item ID
	People      map[int]store.Person      // by TMDB ID
	Collections map[int]store.Collection  // by TMDB ID
	Sidecars    []store.Sidecar           // of music and audiobook libraries
	MetadataDir string
	Log         *slog.Logger
}

// Build assembles a snapshot. Probe data that cannot be parsed is logged and
// treated as missing.
func Build(libs []store.Library, items []store.Item, opt Options) *Snapshot {
	s := &Snapshot{
		byID:     map[string]*Movie{},
		sources:  map[string]*Version{},
		sourceOf: map[*Version]*Movie{},
		parts:    map[string]PartRef{},
		genres:   map[string]*Facet{},
		people:   map[string]*Facet{},
		studios:  map[string]*Facet{},
	}
	counts := map[string]int{}
	var extras, tracks, books []*Movie
	for _, it := range items {
		m := &Movie{Item: it}
		m.Files = nil
		if it.Type == TypeAudio {
			if t := buildTrack(m, it, opt); t != nil {
				tracks = append(tracks, m)
				s.byID[m.ID] = m
			}
			continue
		}
		m.Versions = buildVersions(it, opt.Locale, opt.Log)
		if it.Type == TypeAudioBook {
			if len(m.Versions) > 0 {
				books = append(books, m)
				s.byID[m.ID] = m
				counts[it.LibraryID]++
			}
			continue
		}
		if md, ok := opt.Metadata[it.ID]; ok && it.ParentID == "" {
			m.applyMeta(md, opt)
		}
		m.analyse()
		s.index(m)
		s.byID[m.ID] = m
		if it.ParentID != "" {
			extras = append(extras, m)
			continue
		}
		if len(m.Versions) == 0 {
			continue
		}
		s.movies = append(s.movies, m)
		counts[it.LibraryID]++
	}
	for _, x := range extras {
		if p, ok := s.byID[x.ParentID]; ok && len(x.Versions) > 0 {
			x.Parent = p
			p.Extras = append(p.Extras, x)
		} else {
			delete(s.byID, x.ID)
		}
	}
	for _, m := range s.byID {
		sort.Slice(m.Extras, func(i, j int) bool { return m.Extras[i].SortTitle < m.Extras[j].SortTitle })
		for _, v := range m.Versions {
			s.sources[v.ID] = v
			s.sourceOf[v] = m
			for i, p := range v.Parts {
				s.parts[p.File.ID] = PartRef{Movie: m, Version: v, Index: i}
			}
		}
	}
	sort.Slice(s.movies, func(i, j int) bool { return lessByName(s.movies[i], s.movies[j]) })
	s.locale = opt.Locale
	if len(tracks)+len(books) > 0 {
		s.music = buildMusic(tracks, books, opt)
		for _, al := range s.music.albums {
			counts[al.LibraryID]++
		}
	}
	s.buildCollections(opt)
	for _, f := range s.people {
		if p, ok := opt.People[f.TMDbID]; ok {
			f.applyPerson(p, opt)
		}
	}
	for _, l := range libs {
		s.Libraries = append(s.Libraries, Library{Library: l, Movies: counts[l.ID]})
	}
	return s
}

// buildTrack makes the version and track information of a music file
// from its kept audio metadata; files not probed yet are left out.
func buildTrack(m *Movie, it store.Item, opt Options) *Track {
	if len(it.Files) == 0 || it.Files[0].AudioMeta == "" {
		return nil
	}
	f := it.Files[0]
	var meta media.AudioMeta
	if err := json.Unmarshal([]byte(f.AudioMeta), &meta); err != nil {
		if opt.Log != nil {
			opt.Log.Warn("unreadable audio metadata", "path", f.Path, "err", err)
		}
		return nil
	}
	info := meta.Info(f.Duration, opt.Locale)
	f.AudioMeta = ""
	m.Versions = []*Version{{ID: it.ID, Parts: []Part{{File: f, Info: info}}, Size: f.Size, Duration: info.Duration}}
	t := &Track{Meta: meta, Disc: max(meta.Disc, 1), Number: meta.Track}
	m.Track = t
	if meta.Title != "" {
		m.Title = meta.Title
		m.SortTitle = library.SortKey(meta.Title, opt.Articles)
	}
	m.Year = meta.Year()
	return t
}

func lessByName(a, b *Movie) bool {
	if a.SortTitle != b.SortTitle {
		return a.SortTitle < b.SortTitle
	}
	if a.Year != b.Year {
		return a.Year < b.Year
	}
	return a.ID < b.ID
}

func buildVersions(it store.Item, loc media.Locale, log *slog.Logger) []*Version {
	byLabel := map[string]*Version{}
	var order []*Version
	for _, f := range it.Files {
		v, ok := byLabel[f.Version]
		if !ok {
			v = &Version{Label: f.Version}
			byLabel[f.Version] = v
			order = append(order, v)
		}
		var info media.Info
		if f.Probe != "" {
			var err error
			info, err = media.Parse(f.Probe, loc)
			if err != nil && log != nil {
				log.Warn("unreadable probe data", "path", f.Path, "err", err)
			}
		}
		if info.Duration == 0 {
			info.Duration = f.Duration
		}
		f.Probe = ""
		v.Parts = append(v.Parts, Part{File: f, Info: info})
		v.Size += f.Size
		v.Duration += info.Duration
	}
	for _, v := range order {
		sort.Slice(v.Parts, func(i, j int) bool { return v.Parts[i].File.Part < v.Parts[j].File.Part })
	}
	sort.SliceStable(order, func(i, j int) bool {
		hi, hj := height(order[i]), height(order[j])
		if hi != hj {
			return hi > hj
		}
		return order[i].Label < order[j].Label
	})
	for i, v := range order {
		if i == 0 {
			v.ID = it.ID
		} else {
			v.ID = v.Parts[0].File.ID
		}
	}
	return order
}

func height(v *Version) int {
	if s, ok := v.Info().Video(); ok {
		return s.Height
	}
	return 0
}

func (m *Movie) analyse() {
	m.searchKey = SearchKey(m.Title)
	if m.Meta != nil {
		m.searchKey += " | " + SearchKey(m.Meta.Title) + " | " + SearchKey(m.Meta.OriginalTitle)
	}
	m.audioLangs, m.subtitleLangs = map[string]bool{}, map[string]bool{}
	for _, v := range m.Versions {
		for _, s := range v.Info().Streams {
			switch s.Type {
			case "Video":
				if s.Height > m.maxHeight {
					m.maxHeight = s.Height
				}
			case "Audio":
				m.audioLangs[strings.ToLower(s.Language)] = true
			case "Subtitle":
				m.hasSubtitles = true
				m.subtitleLangs[strings.ToLower(s.Language)] = true
			}
		}
	}
}

// SearchKey lowercases s, removes accents and turns punctuation into
// single spaces, so "Wall·E" and "wall e" compare equal.
func SearchKey(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(norm.NFKD.String(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r == 'ß':
			b.WriteString("ss")
			space = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// Item returns a movie or extra by ID.
func (s *Snapshot) Item(id string) (*Movie, bool) {
	m, ok := s.byID[id]
	return m, ok
}

// Source returns a version by media source ID together with its movie.
func (s *Snapshot) Source(id string) (*Movie, *Version, bool) {
	v, ok := s.sources[id]
	if !ok {
		return nil, nil, false
	}
	return s.sourceOf[v], v, true
}

// Part returns a file of a movie by file ID.
func (s *Snapshot) Part(fileID string) (PartRef, bool) {
	r, ok := s.parts[fileID]
	return r, ok
}

// Library returns a library by ID.
func (s *Snapshot) Library(id string) (Library, bool) {
	for _, l := range s.Libraries {
		if l.ID == id {
			return l, true
		}
	}
	return Library{}, false
}

// AllMovies returns the present top-level movies by sort title.
func (s *Snapshot) AllMovies() []*Movie { return s.movies }
