package catalog

import (
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"homeflix/internal/store"
)

// Audio item types, as Jellyfin names them.
const (
	TypeAudio       = "Audio"
	TypeAlbum       = "MusicAlbum"
	TypeArtist      = "MusicArtist"
	TypeAudioBook   = "AudioBook"
	TypeAlbumArtist = "AlbumArtist" // not an item type: artists that are album artists
)

// AudioItem is one entry of a music or audiobook listing: a track or book,
// an album or an artist; exactly one is set.
type AudioItem struct {
	Item   *Movie // track or audiobook
	Album  *Album
	Artist *Artist
}

// ID returns the item's ID.
func (a AudioItem) ID() string {
	switch {
	case a.Item != nil:
		return a.Item.ID
	case a.Album != nil:
		return a.Album.ID
	case a.Artist != nil:
		return a.Artist.ID
	}
	return ""
}

// audioEntry is what listings filter and sort by.
type audioEntry struct {
	item      AudioItem
	typ       string
	id        string
	sortName  string
	search    string
	libs      map[string]bool
	parent    string // album of a track
	year      int
	premiere  time.Time
	created   time.Time
	runtime   time.Duration
	disc, num int
	album     string // sort name of the album of a track
	albumArt  string // sort name of the album artist
	artist    string // sort name of the first artist
	genres    map[string]bool
	artists   map[string]bool
	albumArts map[string]bool
	tracks    []*Movie // whose play state an album sums up
}

func oneLib(id string) map[string]bool { return map[string]bool{id: true} }

func (x *musicIndex) buildEntries() {
	add := func(e *audioEntry) { x.entries = append(x.entries, e) }
	for _, t := range x.tracks {
		tr := t.Track
		e := &audioEntry{item: AudioItem{Item: t}, typ: TypeAudio, id: t.ID, sortName: t.SortTitle,
			libs: oneLib(t.LibraryID), year: t.Year, premiere: tr.Meta.ReleaseDate(), created: t.DateCreated,
			disc: tr.Disc, num: tr.Number, genres: map[string]bool{}, artists: map[string]bool{},
			albumArts: map[string]bool{}}
		if len(t.Versions) > 0 {
			e.runtime = t.Versions[0].Duration
		}
		e.search = SearchKey(t.Title)
		if al := tr.Album; al != nil {
			e.parent, e.album = al.ID, al.SortName
			e.search += " | " + al.searchKey
			for id := range al.genreIDs {
				e.genres[id] = true
			}
		}
		for _, g := range tr.Meta.Genres {
			e.genres[MusicGenreID(g)] = true
		}
		for _, a := range tr.Artists {
			e.artists[a.ID] = true
			e.search += " | " + a.searchKey
		}
		if len(tr.Artists) > 0 {
			e.artist = tr.Artists[0].SortName
		}
		for _, a := range tr.AlbumArtists {
			e.artists[a.ID], e.albumArts[a.ID] = true, true
		}
		if len(tr.AlbumArtists) > 0 {
			e.albumArt = tr.AlbumArtists[0].SortName
		}
		add(e)
	}
	for _, al := range x.albums {
		e := &audioEntry{item: AudioItem{Album: al}, typ: TypeAlbum, id: al.ID, sortName: al.SortName,
			search: al.searchKey, libs: oneLib(al.LibraryID), year: al.Year, premiere: al.Premiere,
			created: al.DateCreated, runtime: al.Duration, album: al.SortName, genres: al.genreIDs,
			artists: al.artistIDs, albumArts: map[string]bool{}, tracks: al.Tracks}
		for _, a := range al.Artists {
			e.albumArts[a.ID] = true
		}
		if len(al.Artists) > 0 {
			e.albumArt, e.artist = al.Artists[0].SortName, al.Artists[0].SortName
		}
		add(e)
	}
	for _, a := range x.artists {
		e := &audioEntry{item: AudioItem{Artist: a}, typ: TypeArtist, id: a.ID, sortName: a.SortName,
			search: a.searchKey, libs: a.libs, created: a.DateCreated, artist: a.SortName,
			genres: map[string]bool{}, artists: map[string]bool{a.ID: true}, albumArts: map[string]bool{}}
		for _, g := range a.Genres {
			e.genres[MusicGenreID(g)] = true
		}
		if a.albumArtist {
			e.albumArts[a.ID] = true
		}
		add(e)
	}
	for _, bk := range x.books {
		b := bk.Book
		e := &audioEntry{item: AudioItem{Item: bk}, typ: TypeAudioBook, id: bk.ID, sortName: bk.SortTitle,
			search: SearchKey(bk.Title), libs: oneLib(bk.LibraryID), year: bk.Year, premiere: b.Premiere,
			created: bk.DateCreated, runtime: b.Duration, genres: map[string]bool{}, artists: map[string]bool{},
			albumArts: map[string]bool{}}
		for _, g := range b.Genres {
			e.genres[MusicGenreID(g)] = true
		}
		for _, a := range b.Authors {
			e.artists[a.ID], e.albumArts[a.ID] = true, true
			e.search += " | " + a.searchKey
		}
		if len(b.Authors) > 0 {
			e.artist, e.albumArt = b.Authors[0].SortName, b.Authors[0].SortName
		}
		if b.Series != "" {
			e.search += " | " + SearchKey(b.Series)
		}
		add(e)
	}
	sort.SliceStable(x.entries, func(i, j int) bool { return x.entries[i].sortName < x.entries[j].sortName })
}

// AudioQuery selects, orders and pages music and audiobooks.
type AudioQuery struct {
	LibraryID string
	ParentID  string          // an album: its tracks
	Types     map[string]bool // TypeAudio, TypeAlbum, TypeArtist, TypeAudioBook, TypeAlbumArtist
	IDs       []string
	ArtistIDs []string // items by or with any of these artists
	// AlbumArtistIDs restricts to items whose album artist (or author) is
	// one of these.
	AlbumArtistIDs []string
	GenreIDs       []string
	Years          []int
	Search         string

	NameStartsWith           string
	NameStartsWithOrGreater  string
	NameLessThan             string
	MinPremiere, MaxPremiere time.Time

	User      map[string]store.UserData
	Played    *bool
	Favorite  *bool
	Resumable *bool

	SortBy     []string
	Descending []bool
	StartIndex int
	Limit      int
}

// HasAudio reports whether the snapshot has music or audiobooks.
func (s *Snapshot) HasAudio() bool { return s.music != nil && len(s.music.entries) > 0 }

// AudioItems returns the requested page and the number of matches.
func (s *Snapshot) AudioItems(q AudioQuery) ([]AudioItem, int) {
	if s.music == nil {
		return nil, 0
	}
	set := func(list []string) map[string]bool {
		if len(list) == 0 {
			return nil
		}
		m := map[string]bool{}
		for _, v := range list {
			m[strings.ToLower(v)] = true
		}
		return m
	}
	ids, artists, albumArts, genres := set(q.IDs), set(q.ArtistIDs), set(q.AlbumArtistIDs), set(q.GenreIDs)
	years := map[int]bool{}
	for _, y := range q.Years {
		years[y] = true
	}
	search := SearchKey(q.Search)
	starts, orGreater, lessThan := SearchKey(q.NameStartsWith), SearchKey(q.NameStartsWithOrGreater), SearchKey(q.NameLessThan)
	anyOf := func(want, have map[string]bool) bool {
		for k := range want {
			if have[k] {
				return true
			}
		}
		return false
	}
	var out []*audioEntry
	for _, e := range s.music.entries {
		typeOK := q.Types[e.typ] || (e.typ == TypeArtist && q.Types[TypeAlbumArtist] && len(e.albumArts) > 0)
		switch {
		case len(q.Types) > 0 && !typeOK,
			q.LibraryID != "" && !e.libs[q.LibraryID],
			q.ParentID != "" && e.parent != q.ParentID,
			ids != nil && !ids[e.id],
			artists != nil && !anyOf(artists, e.artists),
			albumArts != nil && !anyOf(albumArts, e.albumArts),
			genres != nil && !anyOf(genres, e.genres),
			len(years) > 0 && !years[e.year],
			search != "" && !strings.Contains(e.search, search),
			starts != "" && !strings.HasPrefix(e.sortName, starts),
			orGreater != "" && e.sortName < orGreater,
			lessThan != "" && e.sortName >= lessThan,
			!q.MinPremiere.IsZero() && (e.premiere.IsZero() || e.premiere.Before(q.MinPremiere)),
			!q.MaxPremiere.IsZero() && (e.premiere.IsZero() || e.premiere.After(q.MaxPremiere)),
			q.Played != nil && e.played(q.User) != *q.Played,
			q.Favorite != nil && q.User[e.id].IsFavorite != *q.Favorite,
			q.Resumable != nil && (q.User[e.id].Position > 0) != *q.Resumable:
			continue
		}
		out = append(out, e)
	}
	sortBy := q.SortBy
	if len(sortBy) == 0 && q.ParentID != "" {
		sortBy = []string{"ParentIndexNumber", "IndexNumber"}
	}
	sortAudio(out, sortBy, q.Descending, q.User)
	total := len(out)
	start := min(max(q.StartIndex, 0), total)
	out = out[start:]
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	items := make([]AudioItem, len(out))
	for i, e := range out {
		items[i] = e.item
	}
	return items, total
}

// played reports whether the user has played the item; an album when all
// its tracks are.
func (e *audioEntry) played(user map[string]store.UserData) bool {
	if e.typ == TypeAlbum {
		for _, t := range e.tracks {
			if !user[t.ID].Played {
				return false
			}
		}
		return len(e.tracks) > 0
	}
	return user[e.id].Played
}

func sortAudio(es []*audioEntry, by []string, desc []bool, user map[string]store.UserData) {
	type key struct {
		cmp  func(a, b *audioEntry) int
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
		var cmp func(a, b *audioEntry) int
		switch strings.ToLower(field) {
		case "sortname", "name":
			cmp = func(a, b *audioEntry) int { return strings.Compare(a.sortName, b.sortName) }
		case "album":
			cmp = func(a, b *audioEntry) int { return strings.Compare(a.album, b.album) }
		case "albumartist":
			cmp = func(a, b *audioEntry) int { return strings.Compare(a.albumArt, b.albumArt) }
		case "artist":
			cmp = func(a, b *audioEntry) int { return strings.Compare(a.artist, b.artist) }
		case "parentindexnumber":
			cmp = func(a, b *audioEntry) int { return a.disc - b.disc }
		case "indexnumber":
			cmp = func(a, b *audioEntry) int { return a.num - b.num }
		case "productionyear":
			cmp = func(a, b *audioEntry) int { return a.year - b.year }
		case "premieredate":
			cmp = func(a, b *audioEntry) int {
				if c := a.premiere.Compare(b.premiere); c != 0 {
					return c
				}
				return a.year - b.year
			}
		case "datecreated":
			cmp = func(a, b *audioEntry) int { return a.created.Compare(b.created) }
		case "runtime":
			cmp = func(a, b *audioEntry) int { return compareInt(int64(a.runtime), int64(b.runtime)) }
		case "dateplayed", "datelastcontentadded":
			if strings.EqualFold(field, "datelastcontentadded") {
				cmp = func(a, b *audioEntry) int { return a.created.Compare(b.created) }
				break
			}
			cmp = func(a, b *audioEntry) int { return user[a.id].LastPlayed.Compare(user[b.id].LastPlayed) }
		case "playcount":
			cmp = func(a, b *audioEntry) int { return user[a.id].PlayCount - user[b.id].PlayCount }
		case "isfavoriteorliked":
			cmp = func(a, b *audioEntry) int { return compareBool(user[a.id].IsFavorite, user[b.id].IsFavorite) }
		case "isplayed":
			cmp = func(a, b *audioEntry) int { return compareBool(a.played(user), b.played(user)) }
		case "random":
			rand.Shuffle(len(es), func(i, j int) { es[i], es[j] = es[j], es[i] })
			return
		default:
			continue
		}
		keys = append(keys, key{cmp, d})
	}
	sort.SliceStable(es, func(i, j int) bool {
		for _, k := range keys {
			c := k.cmp(es[i], es[j])
			if k.desc {
				c = -c
			}
			if c != 0 {
				return c < 0
			}
		}
		if es[i].sortName != es[j].sortName {
			return es[i].sortName < es[j].sortName
		}
		return es[i].id < es[j].id
	})
}

// Album returns an album by ID.
func (s *Snapshot) Album(id string) (*Album, bool) {
	if s.music == nil {
		return nil, false
	}
	a, ok := s.music.album[id]
	return a, ok
}

// Artist returns an artist or author by ID.
func (s *Snapshot) Artist(id string) (*Artist, bool) {
	if s.music == nil {
		return nil, false
	}
	a, ok := s.music.artist[id]
	return a, ok
}

// ArtistByName returns the artist of this name, if there is one.
func (s *Snapshot) ArtistByName(name string) (*Artist, bool) {
	if s.music == nil {
		return nil, false
	}
	key := SearchKey(name)
	for _, a := range s.music.artists {
		if a.searchKey == key {
			return a, true
		}
	}
	return nil, false
}

// MusicGenre returns a genre of music or audiobooks by ID.
func (s *Snapshot) MusicGenre(id string) (*Facet, bool) {
	if s.music == nil {
		return nil, false
	}
	f, ok := s.music.genres[id]
	return f, ok
}

// MusicGenres lists the genres of albums (books for audiobooks) of a
// library ("" for all) matching search, by name.
func (s *Snapshot) MusicGenres(libraryID, search string) []*Facet {
	if s.music == nil {
		return nil
	}
	key := SearchKey(search)
	var out []*Facet
	for _, f := range s.music.genres {
		if (libraryID == "" || f.libs[libraryID]) && (key == "" || strings.Contains(SearchKey(f.Name), key)) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Albums, Artists, Tracks and Books return everything of a kind.
func (s *Snapshot) Albums() []*Album {
	if s.music == nil {
		return nil
	}
	return s.music.albums
}

func (s *Snapshot) Artists() []*Artist {
	if s.music == nil {
		return nil
	}
	return s.music.artists
}

func (s *Snapshot) Tracks() []*Movie {
	if s.music == nil {
		return nil
	}
	return s.music.tracks
}

func (s *Snapshot) Books() []*Movie {
	if s.music == nil {
		return nil
	}
	return s.music.books
}

// HasGenre reports whether movies have a genre of this name.
func (s *Snapshot) HasGenre(name string) bool {
	_, ok := s.genres[GenreID(name)]
	return ok
}
