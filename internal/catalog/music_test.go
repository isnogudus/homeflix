package catalog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"homeflix/internal/media"
	"homeflix/internal/store"
)

const (
	musicLib = "21111111111111111111111111111111"
	bookLib  = "31111111111111111111111111111111"
)

// trackItem makes a stored track with the given tags.
func trackItem(id, path string, m media.AudioMeta) store.Item {
	if m.Codec == "" {
		m.Codec, m.Container, m.Channels, m.SampleRate = "mp3", "mp3", 2, 44100
	}
	m.Cover = -1
	if strings.Contains(path, "cover") {
		m.Cover = 1
	}
	b, _ := json.Marshal(m)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(id)) * time.Hour)
	return store.Item{ID: id, LibraryID: musicLib, Type: TypeAudio, Path: path, Title: "file", SortTitle: "file",
		DateCreated: created, Files: []store.MediaFile{{ID: "f" + id, ItemID: id, Path: path, Size: 1000,
			Duration: 3 * time.Minute, AudioMeta: string(b)}}}
}

func musicSnapshot() *Snapshot {
	abba := "d87e52c5-bb8d-4da8-b941-9f4928627dc8"
	items := []store.Item{
		// Two discs in two folders, one album; one track without the MBID.
		trackItem("t1", "/music/Albums/ABBA/Gold/Disc 01/01 Dancing Queen.mp3", media.AudioMeta{Title: "Dancing Queen",
			Artist: "ABBA", AlbumArtist: "ABBA", Album: "Gold", Track: 1, Disc: 1, Date: "1992-09-21",
			Genres: []string{"Pop"}, MBAlbumArtistID: abba, MBArtistIDs: []string{abba}, MBAlbumID: "a1a1a1a1-0000-0000-0000-000000000001"}),
		trackItem("t2", "/music/Albums/ABBA/Gold/Disc 02/01 Voulez-Vous.mp3", media.AudioMeta{Title: "Voulez-Vous",
			Artist: "ABBA", AlbumArtist: "ABBA", Album: "Gold", Track: 1, Disc: 2, Genres: []string{"Pop"},
			MBAlbumID: "a1a1a1a1-0000-0000-0000-000000000001"}),
		trackItem("t3", "/music/Albums/ABBA/Gold/Disc 01/02 Knowing Me, Knowing You.mp3", media.AudioMeta{
			Title: "Knowing Me, Knowing You", Artist: "Abba", AlbumArtist: "ABBA", Album: "Gold", Track: 2, Disc: 1,
			MBAlbumID: "a1a1a1a1-0000-0000-0000-000000000001"}),
		// Another tree, the same artist without IDs.
		trackItem("t4", "/flac/Albums/ABBA/Arrival/01 When I Kissed the Teacher cover.flac", media.AudioMeta{
			Title: "When I Kissed the Teacher", Artist: "ABBA", AlbumArtist: "ABBA", Album: "Arrival", Track: 1, Date: "1976"}),
		// A compilation without album artist, two artists on one track.
		trackItem("t5", "/music/Compilations/Bravo Hits 1/01.mp3", media.AudioMeta{Title: "Take On Me",
			Artist: "a-ha", Album: "Bravo Hits 1", Track: 1, Compilation: true}),
		trackItem("t6", "/music/Compilations/Bravo Hits 1/02.mp3", media.AudioMeta{Title: "Under Pressure",
			Artist: "Queen & David Bowie", Artists: []string{"Queen", "David Bowie"}, Album: "Bravo Hits 1", Track: 2,
			Compilation: true}),
		// No album at all.
		trackItem("t7", "/music/Singletons/demo.mp3", media.AudioMeta{Title: "Demo", Artist: "Nobody"}),
	}
	day := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	sidecars := []store.Sidecar{
		{Path: "/music/Albums/ABBA/Gold/folder.jpg", Dir: "/music/Albums/ABBA/Gold", Name: "folder.jpg", ModTime: day},
		{Path: "/music/Albums/ABBA/Gold/album.nfo", Dir: "/music/Albums/ABBA/Gold", Name: "album.nfo",
			Data: `{"title":["ABBA Gold: Greatest Hits"],"year":["1992"],"genre":["Pop / Disco"],"review":["The hits."]}`},
		{Path: "/music/Albums/ABBA/artist.nfo", Dir: "/music/Albums/ABBA", Name: "artist.nfo",
			Data: `{"name":["ABBA"],"sortname":["Abba"],"biography":["A Swedish group."]}`},
		{Path: "/music/Albums/ABBA/backdrop.jpg", Dir: "/music/Albums/ABBA", Name: "backdrop.jpg", ModTime: day},
		{Path: "/music/Albums/ABBA/logo.png", Dir: "/music/Albums/ABBA", Name: "logo.png", ModTime: day},
	}
	return Build([]store.Library{{ID: musicLib, Name: "Musik", CollectionType: "music"}}, items,
		Options{Locale: "en", Articles: []string{"the"}, Sidecars: sidecars})
}

func audioNames(items []AudioItem) string {
	var out []string
	for _, it := range items {
		switch {
		case it.Album != nil:
			out = append(out, it.Album.Name)
		case it.Artist != nil:
			out = append(out, it.Artist.Name)
		default:
			out = append(out, it.Item.Title)
		}
	}
	return strings.Join(out, ", ")
}

func TestMusicIndex(t *testing.T) {
	s := musicSnapshot()
	if n := len(s.Albums()); n != 3 {
		t.Fatalf("%d albums", n)
	}
	albums, _ := s.AudioItems(AudioQuery{Types: map[string]bool{TypeAlbum: true}, SortBy: []string{"SortName"}})
	if got := audioNames(albums); got != "ABBA Gold: Greatest Hits, Arrival, Bravo Hits 1" {
		t.Errorf("albums: %s", got)
	}
	gold := albums[0].Album
	if gold.Dir != "/music/Albums/ABBA/Gold" || gold.Cover.File != "/music/Albums/ABBA/Gold/folder.jpg" ||
		gold.Overview != "The hits." || strings.Join(gold.Genres, ",") != "Pop,Disco" || gold.Year != 1992 {
		t.Errorf("gold: %+v", gold)
	}
	tracks, _ := s.AudioItems(AudioQuery{ParentID: gold.ID, Types: map[string]bool{TypeAudio: true}})
	if got := audioNames(tracks); got != "Dancing Queen, Knowing Me, Knowing You, Voulez-Vous" {
		t.Errorf("gold tracks by disc: %s", got)
	}
	// The ABBA of both trees, with and without IDs, is one artist.
	abba, ok := s.ArtistByName("abba")
	if !ok || len(abba.Albums) != 2 || len(abba.Tracks) != 4 || abba.Overview != "A Swedish group." ||
		abba.Backdrop.IsZero() || abba.Logo.IsZero() || abba.SortName != "abba" {
		t.Fatalf("abba: %+v", abba)
	}
	if abba.Image("Primary") != gold.Cover && abba.Image("Primary").File == "" {
		t.Errorf("artist picture falls back to an album cover: %+v", abba.Image("Primary"))
	}
	arrival := albums[1].Album
	if arrival.Cover.Embedded == "" || arrival.Cover.Stream != 1 {
		t.Errorf("embedded cover: %+v", arrival.Cover)
	}
	bravo := albums[2].Album
	if len(bravo.Artists) != 1 || bravo.Artists[0].Name != "Various Artists" {
		t.Errorf("compilation artist: %+v", bravo.Artists)
	}
	byQueen, _ := s.AudioItems(AudioQuery{ArtistIDs: []string{ArtistID("Queen", "")}, Types: map[string]bool{TypeAudio: true, TypeAlbum: true}})
	if got := audioNames(byQueen); got != "Bravo Hits 1, Under Pressure" {
		t.Errorf("by Queen: %s", got)
	}
	albumArtists, _ := s.AudioItems(AudioQuery{Types: map[string]bool{TypeAlbumArtist: true}})
	if got := audioNames(albumArtists); got != "ABBA, Various Artists" {
		t.Errorf("album artists: %s", got)
	}
	single, _ := s.AudioItems(AudioQuery{Search: "demo", Types: map[string]bool{TypeAudio: true}})
	if len(single) != 1 || single[0].Item.Track.Album != nil {
		t.Errorf("track without album: %+v", single)
	}
	pop, _ := s.AudioItems(AudioQuery{GenreIDs: []string{MusicGenreID("disco")}, Types: map[string]bool{TypeAlbum: true}})
	if audioNames(pop) != "ABBA Gold: Greatest Hits" {
		t.Errorf("genre filter: %s", audioNames(pop))
	}
	if g := s.MusicGenres(musicLib, ""); len(g) != 2 || g[0].Name != "Disco" || g[0].Albums != 1 {
		t.Errorf("genres: %+v", g)
	}
	user := map[string]store.UserData{"t1": {Played: true}, "t2": {Played: true}, "t3": {Played: true}}
	played, _ := s.AudioItems(AudioQuery{Types: map[string]bool{TypeAlbum: true}, User: user, Played: truePtr()})
	if audioNames(played) != "ABBA Gold: Greatest Hits" {
		t.Errorf("played albums: %s", audioNames(played))
	}
	if _, ok := s.Item("t1"); !ok || len(s.AllMovies()) != 0 {
		t.Error("tracks are items but not movies")
	}
}

func truePtr() *bool { b := true; return &b }

func TestBooks(t *testing.T) {
	meta := func(m media.AudioMeta) string {
		m.Cover = -1
		b, _ := json.Marshal(m)
		return string(b)
	}
	probe := `{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"44100"}],
		"chapters":[{"start_time":"0.0","tags":{"title":"Kapitel 1"}},{"start_time":"600.0","tags":{"title":"Kapitel 2"}}],
		"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"36000.0"}}`
	mp3 := `{"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3"}],"format":{"format_name":"mp3","duration":"300.0"}}`
	items := []store.Item{
		{ID: "b1", LibraryID: bookLib, Type: TypeAudioBook, Path: "/books/Marc Elsberg/Blackout/Blackout.m4b",
			Title: "Blackout", SortTitle: "blackout", Files: []store.MediaFile{{ID: "fb1", ItemID: "b1",
				Path: "/books/Marc Elsberg/Blackout/Blackout.m4b", Probe: probe,
				AudioMeta: meta(media.AudioMeta{Album: "Blackout", AlbumArtist: "Marc Elsberg", Composer: "Steffen Groth", Genres: []string{"Audiobook"}})}}},
		{ID: "b2", LibraryID: bookLib, Type: TypeAudioBook, Path: "/books/Thomas Finn/Nebelkriege 2",
			Title: "Nebelkriege 2", SortTitle: "nebelkriege 2", Files: []store.MediaFile{
				{ID: "fb2a", ItemID: "b2", Part: 1, Path: "/books/Thomas Finn/Nebelkriege 2/01 Prolog.mp3", Probe: mp3,
					AudioMeta: meta(media.AudioMeta{Artist: "Thomas Finn", Album: "Der eisige Schatten"})},
				{ID: "fb2b", ItemID: "b2", Part: 2, Path: "/books/Thomas Finn/Nebelkriege 2/02 Kapitel.mp3", Probe: mp3}}},
	}
	sidecars := []store.Sidecar{
		{Path: "/books/Marc Elsberg/Blackout/cover.jpg", Dir: "/books/Marc Elsberg/Blackout", Name: "cover.jpg"},
		{Path: "/books/Marc Elsberg/Blackout/metadata.json", Dir: "/books/Marc Elsberg/Blackout", Name: "metadata.json",
			Data: `{"title":"Blackout","subtitle":"Morgen ist es zu spät","authors":["Marc Elsberg"],"narrators":["Steffen Groth"],` +
				`"series":["Thriller #2"],"genres":["Audiobook","Thriller"],"publishedYear":"2012","description":"Zuerst ging das Licht aus."}`},
	}
	s := Build([]store.Library{{ID: bookLib, Name: "Hörbücher", CollectionType: "books"}}, items,
		Options{Locale: "de", Sidecars: sidecars})
	b, ok := s.Item("b1")
	if !ok || b.Book == nil {
		t.Fatal("no book")
	}
	bk := b.Book
	if bk.Subtitle != "Morgen ist es zu spät" || bk.Series != "Thriller" || bk.SeriesIndex != "2" || b.Year != 2012 ||
		strings.Join(bk.Genres, ",") != "Thriller" || len(bk.Authors) != 1 || bk.Authors[0].Name != "Marc Elsberg" ||
		len(bk.Chapters) != 2 || bk.Cover.File == "" || bk.Overview != "Zuerst ging das Licht aus." || bk.Duration != 10*time.Hour {
		t.Errorf("blackout: %+v", bk)
	}
	n, _ := s.Item("b2")
	if n.Title != "Der eisige Schatten" || len(n.Book.Chapters) != 2 || n.Book.Chapters[1].Start != 5*time.Minute ||
		n.Book.Chapters[1].Name != "02 Kapitel" || n.Book.Duration != 10*time.Minute {
		t.Errorf("folder book: %q %+v", n.Title, n.Book)
	}
	authors, _ := s.AudioItems(AudioQuery{LibraryID: bookLib, Types: map[string]bool{TypeArtist: true}})
	if audioNames(authors) != "Marc Elsberg, Thomas Finn" {
		t.Errorf("authors: %s", audioNames(authors))
	}
}
