package catalog

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/library"
	"homeflix/internal/media"
	"homeflix/internal/store"
)

// Art is a picture of an album, artist or book: a file next to the
// media, or the picture embedded in an audio file.
type Art struct {
	File     string // picture file, "" if embedded
	Embedded string // audio file holding the picture
	Stream   int    // stream index of the embedded picture
	Stamp    int64  // modification time (Unix ms) of the file the picture is in
}

// IsZero reports whether there is no picture.
func (a Art) IsZero() bool { return a.File == "" && a.Embedded == "" }

// Track is what a file of a music library adds to its item.
type Track struct {
	Meta         media.AudioMeta
	Album        *Album // nil for tracks without an album tag
	Artists      []*Artist
	AlbumArtists []*Artist
	Disc, Number int
	Lyrics       string // path of an .lrc file, "" if none
}

// Album is a release, made of the tracks that share its tags.
type Album struct {
	ID, LibraryID  string
	Name, SortName string
	Year           int
	Premiere       time.Time
	DateCreated    time.Time
	Artists        []*Artist // album artists
	Tracks         []*Movie  // by disc and number
	Genres         []string
	Overview       string
	MBID           string
	MBReleaseGroup string
	Dir            string
	Cover          Art
	Duration       time.Duration

	searchKey string
	genreIDs  map[string]bool
	artistIDs map[string]bool // album artists and track artists
}

// Artist is a musician or band, or the author of audiobooks.
type Artist struct {
	ID, Name, SortName string
	MBID               string
	Overview           string
	Genres             []string
	Dir                string
	Primary, Backdrop  Art
	Logo               Art
	DateCreated        time.Time

	Albums []*Album // as album artist, newest first
	Tracks []*Movie // tracks naming the artist
	Books  []*Movie // audiobooks by the author

	libs        map[string]bool
	albumArtist bool
	searchKey   string
}

// IsAlbumArtist reports whether the artist is the album artist of an album.
func (a *Artist) IsAlbumArtist() bool { return a.albumArtist }

// InLibrary reports whether the artist has albums, tracks or books in the
// library.
func (a *Artist) InLibrary(id string) bool { return a.libs[id] }

// Image returns the picture of a kind ("Primary", "Backdrop", "Logo"):
// the artist's own, else for Primary the cover of the newest album.
func (a *Artist) Image(kind string) Art {
	switch kind {
	case "Primary":
		if !a.Primary.IsZero() {
			return a.Primary
		}
		for _, al := range a.Albums {
			if !al.Cover.IsZero() {
				return al.Cover
			}
		}
		for _, b := range a.Books {
			if b.Book != nil && !b.Book.Cover.IsZero() {
				return b.Book.Cover
			}
		}
	case "Backdrop":
		return a.Backdrop
	case "Logo":
		return a.Logo
	}
	return Art{}
}

// Book is what an audiobook adds to its item.
type Book struct {
	Subtitle    string
	Authors     []*Artist
	Narrators   []string
	Series      string
	SeriesIndex string
	Overview    string
	Publisher   string
	Genres      []string
	Tags        []string
	Premiere    time.Time
	Chapters    []media.Chapter // over the whole book
	Cover       Art
	Duration    time.Duration
}

// musicIndex holds the music and audiobooks of a snapshot.
type musicIndex struct {
	tracks  []*Movie
	books   []*Movie
	albums  []*Album
	artists []*Artist
	album   map[string]*Album
	artist  map[string]*Artist
	genres  map[string]*Facet
	entries []*audioEntry // everything listable, by sort name
}

// discFolder matches folders that hold one disc of an album.
var discFolder = regexp.MustCompile(`(?i)^(cd|disc|disk|dvd|part|teil)[\s._-]*\d+(\b.*)?$`)

// albumDir is the folder of an album: the folder of its tracks, or above
// it for disc folders.
func albumDir(trackPath string) string {
	dir := filepath.Dir(trackPath)
	if discFolder.MatchString(filepath.Base(dir)) {
		return filepath.Dir(dir)
	}
	return dir
}

// variousArtists is the MusicBrainz artist of compilations.
const variousArtists = "89ad4ac3-39f7-470e-963a-56509c546377"

// nfoValue returns the first value of an .nfo field.
func nfoValue(f map[string][]string, keys ...string) string {
	for _, k := range keys {
		if v := f[k]; len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func parseNFO(data string) map[string][]string {
	var f map[string][]string
	if data == "" || json.Unmarshal([]byte(data), &f) != nil {
		return nil
	}
	return f
}

// sidecarDir indexes sidecar files by folder.
type sidecarDir map[string]map[string]store.Sidecar

func newSidecarDir(list []store.Sidecar) sidecarDir {
	d := sidecarDir{}
	for _, sc := range list {
		if d[sc.Dir] == nil {
			d[sc.Dir] = map[string]store.Sidecar{}
		}
		d[sc.Dir][sc.Name] = sc
	}
	return d
}

// picture returns the first of the named pictures in dir.
func (d sidecarDir) picture(dir string, names ...string) Art {
	for _, n := range names {
		if sc, ok := d[dir][n]; ok {
			return Art{File: sc.Path, Stamp: sc.ModTime.UnixMilli()}
		}
	}
	return Art{}
}

func (d sidecarDir) nfo(dir, name string) map[string][]string {
	if sc, ok := d[dir][name]; ok {
		return parseNFO(sc.Data)
	}
	return nil
}

var (
	coverNames    = []string{"folder.jpg", "folder.jpeg", "folder.png", "cover.jpg", "cover.jpeg", "cover.png", "front.jpg", "front.png"}
	artistNames   = []string{"folder.jpg", "folder.jpeg", "folder.png", "artist.jpg", "artist.png"}
	backdropNames = []string{"backdrop.jpg", "backdrop.png", "fanart.jpg"}
)

// musicBuilder assembles the music index.
type musicBuilder struct {
	idx      *musicIndex
	opt      Options
	sidecars sidecarDir
	// mbidByName links artist names to the MusicBrainz IDs tracks give
	// them, so tracks without IDs join the same artist.
	mbidByName map[string]string
	// bookDirs counts the books per folder; metadata.json and cover.jpg
	// belong to a book only when it is alone in its folder.
	bookDirs map[string]int
}

func artistKey(name, mbid string) string {
	if mbid != "" && mbid != variousArtists {
		return "mb:" + mbid
	}
	if mbid == variousArtists {
		return "mb:" + variousArtists
	}
	return "name:" + SearchKey(name)
}

// ArtistID returns the ID of an artist by name and MusicBrainz ID.
func ArtistID(name, mbid string) string { return facetID("artist", artistKey(name, mbid)) }

func (b *musicBuilder) artistFor(name, mbid, libraryID string, created time.Time) *Artist {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if mbid == "" {
		mbid = b.mbidByName[SearchKey(name)]
	}
	id := ArtistID(name, mbid)
	a, ok := b.idx.artist[id]
	if !ok {
		a = &Artist{ID: id, Name: name, MBID: mbid, libs: map[string]bool{}, DateCreated: created}
		b.idx.artist[id] = a
		b.idx.artists = append(b.idx.artists, a)
	}
	a.libs[libraryID] = true
	if !created.IsZero() && (a.DateCreated.IsZero() || created.Before(a.DateCreated)) {
		a.DateCreated = created
	}
	return a
}

// buildMusic makes tracks, albums, artists and books of the items of
// music and audiobook libraries.
func buildMusic(tracks, books []*Movie, opt Options) *musicIndex {
	b := &musicBuilder{
		idx: &musicIndex{album: map[string]*Album{}, artist: map[string]*Artist{}, genres: map[string]*Facet{}},
		opt: opt, sidecars: newSidecarDir(opt.Sidecars), mbidByName: map[string]string{},
	}
	for _, t := range tracks {
		m := t.Track.Meta
		if m.MBAlbumArtistID != "" && m.AlbumArtist != "" && !strings.Contains(m.AlbumArtist, ";") {
			b.mbidByName[SearchKey(m.AlbumArtist)] = m.MBAlbumArtistID
		}
		names := m.AllArtists()
		if len(names) == len(m.MBArtistIDs) {
			for i, n := range names {
				b.mbidByName[SearchKey(n)] = m.MBArtistIDs[i]
			}
		}
	}
	for _, t := range tracks {
		b.addTrack(t)
	}
	for _, al := range b.idx.albums {
		b.finishAlbum(al)
	}
	b.bookDirs = map[string]int{}
	for _, bk := range books {
		b.bookDirs[bookDir(bk)]++
	}
	for _, bk := range books {
		b.addBook(bk)
	}
	for _, a := range b.idx.artists {
		b.finishArtist(a)
	}
	sort.Slice(b.idx.tracks, func(i, j int) bool { return lessTrack(b.idx.tracks[i], b.idx.tracks[j]) })
	sort.Slice(b.idx.albums, func(i, j int) bool { return b.idx.albums[i].SortName < b.idx.albums[j].SortName })
	sort.Slice(b.idx.artists, func(i, j int) bool { return b.idx.artists[i].SortName < b.idx.artists[j].SortName })
	sort.Slice(b.idx.books, func(i, j int) bool { return lessByName(b.idx.books[i], b.idx.books[j]) })
	b.idx.buildEntries()
	return b.idx
}

func lessTrack(a, b *Movie) bool {
	ta, tb := a.Track, b.Track
	if ta.Album != tb.Album {
		switch {
		case ta.Album == nil:
			return false
		case tb.Album == nil:
			return true
		case ta.Album.SortName != tb.Album.SortName:
			return ta.Album.SortName < tb.Album.SortName
		}
		return ta.Album.ID < tb.Album.ID
	}
	if ta.Disc != tb.Disc {
		return ta.Disc < tb.Disc
	}
	if ta.Number != tb.Number {
		return ta.Number < tb.Number
	}
	return lessByName(a, b)
}

func (b *musicBuilder) addTrack(t *Movie) {
	tr := t.Track
	m := tr.Meta
	created := t.DateCreated
	for i, name := range m.AllArtists() {
		id := ""
		if len(m.MBArtistIDs) == len(m.AllArtists()) {
			id = m.MBArtistIDs[i]
		}
		if a := b.artistFor(name, id, t.LibraryID, created); a != nil {
			tr.Artists = append(tr.Artists, a)
			a.Tracks = append(a.Tracks, t)
		}
	}
	if lrc := strings.TrimSuffix(t.Path, filepath.Ext(t.Path)) + ".lrc"; b.sidecars[filepath.Dir(t.Path)] != nil {
		if _, ok := b.sidecars[filepath.Dir(t.Path)][strings.ToLower(filepath.Base(lrc))]; ok {
			tr.Lyrics = b.sidecars[filepath.Dir(t.Path)][strings.ToLower(filepath.Base(lrc))].Path
		}
	}
	if m.Album == "" {
		tr.AlbumArtists = tr.Artists
		b.idx.tracks = append(b.idx.tracks, t)
		return
	}
	albumArtist, albumArtistID := m.AlbumArtist, m.MBAlbumArtistID
	if albumArtist == "" && m.Compilation {
		albumArtist, albumArtistID = "Various Artists", variousArtists
	}
	if albumArtist == "" {
		albumArtist = m.Artist
	}
	var key string
	if m.MBAlbumID != "" {
		key = "mb:" + m.MBAlbumID
	} else {
		key = "tag:" + SearchKey(albumArtist) + "\x00" + SearchKey(m.Album)
	}
	id := facetID("album", key)
	al, ok := b.idx.album[id]
	if !ok {
		al = &Album{ID: id, LibraryID: t.LibraryID, Name: m.Album, MBID: m.MBAlbumID, MBReleaseGroup: m.MBReleaseGroup,
			Dir: albumDir(t.Path), genreIDs: map[string]bool{}, artistIDs: map[string]bool{}, DateCreated: created}
		if a := b.artistFor(albumArtist, albumArtistID, t.LibraryID, created); a != nil {
			al.Artists = append(al.Artists, a)
		}
		b.idx.album[id] = al
		b.idx.albums = append(b.idx.albums, al)
	}
	if created.Before(al.DateCreated) {
		al.DateCreated = created
	}
	tr.Album = al
	tr.AlbumArtists = al.Artists
	al.Tracks = append(al.Tracks, t)
	b.idx.tracks = append(b.idx.tracks, t)
}

// finishAlbum orders the tracks and takes what the tags and album.nfo say
// about the album as a whole; the .nfo file takes precedence.
func (b *musicBuilder) finishAlbum(al *Album) {
	sort.Slice(al.Tracks, func(i, j int) bool {
		ti, tj := al.Tracks[i].Track, al.Tracks[j].Track
		if ti.Disc != tj.Disc {
			return ti.Disc < tj.Disc
		}
		if ti.Number != tj.Number {
			return ti.Number < tj.Number
		}
		return library.NaturalLess(filepath.Base(al.Tracks[i].Path), filepath.Base(al.Tracks[j].Path))
	})
	first := al.Tracks[0].Track.Meta
	al.Year, al.Premiere = first.Year(), first.ReleaseDate()
	seenGenre := map[string]bool{}
	sortName := first.AlbumSort
	for _, t := range al.Tracks {
		m := t.Track.Meta
		al.Duration += t.Versions[0].Duration
		if al.Year == 0 {
			al.Year, al.Premiere = m.Year(), m.ReleaseDate()
		}
		for _, g := range m.Genres {
			if !seenGenre[strings.ToLower(g)] {
				seenGenre[strings.ToLower(g)] = true
				al.Genres = append(al.Genres, g)
			}
		}
		if al.Cover.IsZero() && m.Cover >= 0 {
			al.Cover = Art{Embedded: t.Path, Stream: m.Cover, Stamp: t.Versions[0].Parts[0].File.ModTime.UnixMilli()}
		}
	}
	dirs := []string{al.Dir}
	for _, t := range al.Tracks {
		if d := filepath.Dir(t.Path); d != al.Dir && len(dirs) < 4 {
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		if pic := b.sidecars.picture(d, coverNames...); !pic.IsZero() {
			al.Cover = pic
			break
		}
	}
	if nfo := b.sidecars.nfo(al.Dir, "album.nfo"); nfo != nil {
		if v := nfoValue(nfo, "title"); v != "" {
			al.Name = v
		}
		if v := nfoValue(nfo, "sorttitle"); v != "" {
			sortName = v
		}
		if y, err := strconv.Atoi(nfoValue(nfo, "year")); err == nil && y > 0 {
			al.Year = y
		}
		for _, k := range []string{"releasedate", "premiered"} {
			if t, err := time.Parse("2006-01-02", firstN(nfoValue(nfo, k), 10)); err == nil {
				al.Premiere = t
				if al.Year == 0 {
					al.Year = t.Year()
				}
				break
			}
		}
		if g := nfo["genre"]; len(g) > 0 {
			al.Genres = splitGenres(g)
		}
		if v := nfoValue(nfo, "review", "outline", "plot"); v != "" {
			al.Overview = v
		}
		if v := nfoValue(nfo, "musicbrainzalbumid"); v != "" && al.MBID == "" {
			al.MBID = strings.ToLower(v)
		}
	}
	if sortName == "" {
		sortName = al.Name
	}
	al.SortName = library.SortKey(sortName, b.opt.Articles)
	for _, g := range al.Genres {
		id := MusicGenreID(g)
		al.genreIDs[id] = true
		f, ok := b.idx.genres[id]
		if !ok {
			f = &Facet{ID: id, Name: g, libs: map[string]bool{}}
			b.idx.genres[id] = f
		}
		f.Albums++
		f.libs[al.LibraryID] = true
	}
	al.searchKey = SearchKey(al.Name)
	for _, a := range al.Artists {
		al.artistIDs[a.ID] = true
		a.albumArtist = true
		a.Albums = append(a.Albums, al)
		al.searchKey += " | " + SearchKey(a.Name)
	}
	for _, t := range al.Tracks {
		for _, a := range t.Track.Artists {
			al.artistIDs[a.ID] = true
		}
	}
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// splitGenres splits genres listed as "Rock / Pop" or "Rock; Pop".
func splitGenres(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range list {
		for _, p := range strings.FieldsFunc(g, func(r rune) bool { return r == ';' || r == '/' || r == '|' }) {
			p = strings.TrimSpace(p)
			if p != "" && !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// finishArtist finds the artist's folder, pictures and artist.nfo. The
// folder is the one above an album folder that is named after the artist
// or holds an artist.nfo.
func (b *musicBuilder) finishArtist(a *Artist) {
	key := SearchKey(a.Name)
	for _, al := range a.Albums {
		parent := filepath.Dir(al.Dir)
		if _, ok := b.sidecars[parent]["artist.nfo"]; ok || SearchKey(filepath.Base(parent)) == key {
			a.Dir = parent
			break
		}
	}
	sort.SliceStable(a.Albums, func(i, j int) bool {
		if a.Albums[i].Year != a.Albums[j].Year {
			return a.Albums[i].Year > a.Albums[j].Year
		}
		return a.Albums[i].SortName < a.Albums[j].SortName
	})
	sortName := ""
	for _, t := range a.Tracks {
		m := t.Track.Meta
		if len(t.Track.Artists) == 1 && m.ArtistSort != "" {
			sortName = m.ArtistSort
			break
		}
	}
	if sortName == "" {
		for _, al := range a.Albums {
			if m := al.Tracks[0].Track.Meta; m.AlbumArtistSort != "" && len(al.Artists) == 1 {
				sortName = m.AlbumArtistSort
				break
			}
		}
	}
	if a.Dir != "" {
		a.Primary = b.sidecars.picture(a.Dir, artistNames...)
		a.Backdrop = b.sidecars.picture(a.Dir, backdropNames...)
		a.Logo = b.sidecars.picture(a.Dir, "logo.png")
		if nfo := b.sidecars.nfo(a.Dir, "artist.nfo"); nfo != nil {
			if v := nfoValue(nfo, "name", "title"); v != "" && SearchKey(v) == key {
				a.Name = v
			}
			if v := nfoValue(nfo, "sortname"); v != "" {
				sortName = v
			}
			if v := nfoValue(nfo, "biography", "review", "outline", "plot"); v != "" {
				a.Overview = v
			}
			if g := nfo["genre"]; len(g) > 0 {
				a.Genres = splitGenres(g)
			}
			if v := nfoValue(nfo, "musicbrainzartistid"); v != "" && a.MBID == "" {
				a.MBID = strings.ToLower(v)
			}
		}
	}
	if len(a.Genres) == 0 {
		seen := map[string]bool{}
		for _, al := range a.Albums {
			for _, g := range al.Genres {
				if !seen[strings.ToLower(g)] {
					seen[strings.ToLower(g)] = true
					a.Genres = append(a.Genres, g)
				}
			}
		}
	}
	if sortName == "" {
		sortName = a.Name
	}
	a.SortName = library.SortKey(sortName, b.opt.Articles)
	a.searchKey = SearchKey(a.Name)
}

// absMetadata is the metadata.json Audiobookshelf writes next to a book.
type absMetadata struct {
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle"`
	Authors       []string `json:"authors"`
	Narrators     []string `json:"narrators"`
	Series        []string `json:"series"`
	Genres        []string `json:"genres"`
	Tags          []string `json:"tags"`
	PublishedYear string   `json:"publishedYear"`
	PublishedDate *string  `json:"publishedDate"`
	Publisher     string   `json:"publisher"`
	Description   string   `json:"description"`
	Chapters      []struct {
		Start float64 `json:"start"`
		Title string  `json:"title"`
	} `json:"chapters"`
}

// seriesIndex splits "Die Götterkriege #3" into series and index.
var seriesIndex = regexp.MustCompile(`^(.*?)\s*#\s*([\d.]+)\s*$`)

// addBook describes an audiobook from its metadata.json, its tags and its
// folder name, in this order, and gives it its chapters and cover.
func (b *musicBuilder) addBook(bk *Movie) {
	book := &Book{}
	bk.Book = book
	b.idx.books = append(b.idx.books, bk)
	if len(bk.Versions) == 0 {
		return
	}
	v := bk.Versions[0]
	book.Duration = v.Duration
	dir := bookDir(bk)
	alone := b.bookDirs[dir] == 1
	var abs *absMetadata
	if sc, ok := b.sidecars[dir]["metadata.json"]; ok && alone && sc.Data != "" {
		var m absMetadata
		if json.Unmarshal([]byte(sc.Data), &m) == nil {
			abs = &m
		}
	}
	var meta media.AudioMeta
	if f := v.Parts[0].File; f.AudioMeta != "" {
		_ = json.Unmarshal([]byte(f.AudioMeta), &meta)
	}
	title := meta.Album
	if title == "" {
		title = meta.Title
	}
	authors := []string{}
	if meta.AlbumArtist != "" {
		authors = append(authors, meta.AlbumArtist)
	} else if meta.Artist != "" {
		authors = append(authors, meta.Artist)
	}
	if meta.Composer != "" {
		book.Narrators = []string{meta.Composer}
	}
	book.Genres = meta.Genres
	book.Overview = meta.Comment
	book.Premiere = meta.ReleaseDate()
	if abs != nil {
		if abs.Title != "" {
			title = abs.Title
		}
		book.Subtitle = abs.Subtitle
		if len(abs.Authors) > 0 {
			authors = abs.Authors
		}
		if len(abs.Narrators) > 0 {
			book.Narrators = abs.Narrators
		}
		if len(abs.Series) > 0 {
			book.Series = abs.Series[0]
			if m := seriesIndex.FindStringSubmatch(book.Series); m != nil {
				book.Series, book.SeriesIndex = m[1], m[2]
			}
		}
		if len(abs.Genres) > 0 {
			book.Genres = abs.Genres
		}
		book.Tags = abs.Tags
		if abs.Description != "" {
			book.Overview = abs.Description
		}
		book.Publisher = abs.Publisher
		if abs.PublishedDate != nil {
			if t, err := time.Parse("2006-01-02", firstN(*abs.PublishedDate, 10)); err == nil {
				book.Premiere = t
			}
		}
		if y, err := strconv.Atoi(abs.PublishedYear); err == nil && y > 0 {
			bk.Year = y
			if book.Premiere.IsZero() {
				book.Premiere = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
			}
		}
	}
	// Genre "Audiobook" says nothing in an audiobook library.
	var genres []string
	for _, g := range splitGenres(book.Genres) {
		if !strings.EqualFold(g, "audiobook") && !strings.EqualFold(g, "hörbuch") {
			genres = append(genres, g)
		}
	}
	book.Genres = genres
	if bk.Year == 0 && !book.Premiere.IsZero() {
		bk.Year = book.Premiere.Year()
	}
	if strings.TrimSpace(title) != "" {
		bk.Title = strings.TrimSpace(title)
		bk.SortTitle = library.SortKey(bk.Title, b.opt.Articles)
	}
	for _, name := range authors {
		for _, n := range strings.Split(name, ";") {
			if a := b.artistFor(n, "", bk.LibraryID, bk.DateCreated); a != nil {
				book.Authors = append(book.Authors, a)
				a.Books = append(a.Books, bk)
			}
		}
	}
	// Chapters: those of the files, else those of metadata.json, else one
	// per file of a book made of several.
	var offset time.Duration
	for i, p := range v.Parts {
		for _, c := range p.Info.Chapters {
			book.Chapters = append(book.Chapters, media.Chapter{Start: offset + c.Start, Name: c.Name})
		}
		if len(p.Info.Chapters) == 0 && len(v.Parts) > 1 {
			name := library.CleanTitle(strings.TrimSuffix(filepath.Base(p.File.Path), filepath.Ext(p.File.Path)))
			if name == "" {
				name = fmt.Sprintf("%d", i+1)
			}
			book.Chapters = append(book.Chapters, media.Chapter{Start: offset, Name: name})
		}
		offset += p.Info.Duration
	}
	if len(book.Chapters) == 0 && abs != nil {
		for _, c := range abs.Chapters {
			book.Chapters = append(book.Chapters, media.Chapter{Start: time.Duration(c.Start * float64(time.Second)), Name: c.Title})
		}
	}
	if alone {
		book.Cover = b.sidecars.picture(dir, coverNames...)
	}
	if book.Cover.IsZero() {
		for _, p := range v.Parts {
			for _, s := range p.Info.Streams {
				if s.Type == "EmbeddedImage" {
					book.Cover = Art{Embedded: p.File.Path, Stream: s.Index, Stamp: p.File.ModTime.UnixMilli()}
					break
				}
			}
			if !book.Cover.IsZero() {
				break
			}
		}
	}
	for _, g := range book.Genres {
		id := MusicGenreID(g)
		f, ok := b.idx.genres[id]
		if !ok {
			f = &Facet{ID: id, Name: g, libs: map[string]bool{}}
			b.idx.genres[id] = f
		}
		f.Books++
		f.libs[bk.LibraryID] = true
	}
}

// bookDir is the folder of a book: the book itself when it is a folder of
// files, else the folder of its file.
func bookDir(bk *Movie) string {
	if len(bk.Files) == 1 && bk.Files[0].Path == bk.Path || len(bk.Versions) > 0 &&
		len(bk.Versions[0].Parts) == 1 && bk.Versions[0].Parts[0].File.Path == bk.Path {
		return filepath.Dir(bk.Path)
	}
	return bk.Path
}
