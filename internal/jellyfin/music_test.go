package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/hls"
	"homeflix/internal/media"
	"homeflix/internal/store"
)

const (
	musicID = "21111111111111111111111111111111"
	booksID = "31111111111111111111111111111111"
)

// musicServer serves a music library with an MP3 album of two tracks and
// a WMA track, and an audiobook library with one book.
func musicServer(t *testing.T) (*Server, *apiClient, string) {
	t.Helper()
	s := newTestServer(t, nil)
	s.HLS = &hls.Manager{FFmpeg: "ffmpeg", Dir: t.TempDir(), Workers: 1}
	dir := t.TempDir()
	item := func(id, lib, typ, name string, m media.AudioMeta, probe string) store.Item {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte("audio data of "+name), 0o644)
		m.Cover = -1
		b, _ := json.Marshal(m)
		return store.Item{ID: id, LibraryID: lib, Type: typ, Path: path, Title: name, SortTitle: name,
			DateCreated: time.Date(2026, 3, len(id)%28+1, 0, 0, 0, 0, time.UTC),
			Files: []store.MediaFile{{ID: "f" + id, ItemID: id, Path: path, Size: 20, Duration: 200 * time.Second,
				AudioMeta: string(b), Probe: probe}}}
	}
	gain := -6.5
	mp3 := func(title string, n int) media.AudioMeta {
		return media.AudioMeta{Title: title, Artist: "Toto", AlbumArtist: "Toto", Album: "IV", Track: n, Date: "1982",
			Genres: []string{"Rock"}, Codec: "mp3", Container: "mp3", Channels: 2, SampleRate: 44100, TrackGain: &gain}
	}
	items := []store.Item{
		item("a1000000000000000000000000000001", musicID, "Audio", "02 Africa.mp3", mp3("Africa", 2), ""),
		item("a1000000000000000000000000000002", musicID, "Audio", "01 Rosanna.mp3", mp3("Rosanna", 1), ""),
		item("a1000000000000000000000000000003", musicID, "Audio", "theme.wma", media.AudioMeta{Title: "Theme",
			Artist: "John Williams", AlbumArtist: "John Williams", Album: "Jurassic Park", Track: 1,
			Codec: "wmav2", Container: "asf", Channels: 2, SampleRate: 44100}, ""),
		item("b1000000000000000000000000000001", booksID, "AudioBook", "Blackout.m4b", media.AudioMeta{Album: "Blackout",
			AlbumArtist: "Marc Elsberg", Codec: "aac", Container: "mov,mp4,m4a,3gp,3g2,mj2"},
			`{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","channels":2}],`+
				`"chapters":[{"start_time":"0","tags":{"title":"Eins"}},{"start_time":"100","tags":{"title":"Zwei"}}],`+
				`"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"200"}}`),
	}
	lrc := filepath.Join(dir, "01 Rosanna.lrc")
	os.WriteFile(lrc, []byte("[ti:Rosanna]\n[00:01.00]All I wanna do\n[00:03.50]is when I wake up\n"), 0o644)
	s.Catalog.Set(catalog.Build([]store.Library{
		{ID: musicID, Name: "Musik", CollectionType: "music"},
		{ID: booksID, Name: "Hörbücher", CollectionType: "books"},
	}, items, catalog.Options{Locale: "en", Sidecars: []store.Sidecar{{Path: lrc, Dir: dir, Name: "01 rosanna.lrc"}}}))
	h := s.Handler()
	u := addUser(t, s, "anna", "b", false)
	c := &apiClient{t: t, h: h}
	login(t, c, "anna", "b")
	return s, c, u.ID
}

func TestMusicListings(t *testing.T) {
	s, c, uid := musicServer(t)
	views := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/UserViews?userId="+uid, "", nil))
	types := map[string]string{}
	for _, v := range views.Items {
		types[v.Name] = v.CollectionType
	}
	if types["Musik"] != "music" || types["Hörbücher"] != "books" {
		t.Errorf("views: %v", types)
	}
	list := func(q string) QueryResult[BaseItemDto] {
		t.Helper()
		return decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Users/"+uid+"/Items?"+q, "", nil))
	}
	albums := list("ParentId=" + musicID + "&IncludeItemTypes=MusicAlbum&Recursive=true&SortBy=SortName&EnableTotalRecordCount=true")
	if names(albums.Items) != "IV, Jurassic Park" || albums.TotalRecordCount != 2 {
		t.Fatalf("albums: %s (%d)", names(albums.Items), albums.TotalRecordCount)
	}
	iv := albums.Items[0]
	if iv.Type != "MusicAlbum" || !iv.IsFolder || *iv.ChildCount != 2 || iv.AlbumArtist != "Toto" ||
		len(iv.AlbumArtists) != 1 || iv.ProductionYear != 1982 {
		t.Errorf("album: %+v", iv)
	}
	// Moonfin lists an album's tracks without Recursive and few fields.
	tracks := list("ParentId=" + iv.ID + "&IncludeItemTypes=Audio&SortBy=ParentIndexNumber,IndexNumber,SortName&Fields=PrimaryImageAspectRatio")
	if names(tracks.Items) != "Rosanna, Africa" {
		t.Fatalf("tracks: %s", names(tracks.Items))
	}
	tr := tracks.Items[0]
	if tr.MediaType != "Audio" || tr.AlbumID != iv.ID || tr.Album != "IV" || *tr.IndexNumber != 1 ||
		*tr.ParentIndexNumber != 1 || tr.RunTimeTicks != ticks(200*time.Second) || tr.AlbumArtists[0].ID != iv.AlbumArtists[0].ID ||
		len(tr.ArtistItems) != 1 || tr.ParentID != iv.ID {
		t.Errorf("track: %+v", tr)
	}
	// Songs are counted with Limit=0.
	if res := list("ParentId=" + musicID + "&IncludeItemTypes=Audio&Recursive=true&Limit=0"); res.TotalRecordCount != 3 || len(res.Items) != 0 {
		t.Errorf("song count %d with %d items", res.TotalRecordCount, len(res.Items))
	}
	artist := iv.AlbumArtists[0].ID
	if got := names(list("ArtistIds=" + artist + "&IncludeItemTypes=MusicAlbum&Recursive=true").Items); got != "IV" {
		t.Errorf("albums of artist: %s", got)
	}
	aa := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Artists/AlbumArtists?ParentId="+musicID+"&UserId="+uid+"&SortBy=SortName", "", nil))
	if names(aa.Items) != "John Williams, Toto" || aa.Items[1].Type != "MusicArtist" || *aa.Items[1].AlbumCount != 1 {
		t.Errorf("album artists: %s", names(aa.Items))
	}
	if d := decode[BaseItemDto](t, c.do("GET", "/Users/"+uid+"/Items/"+artist, "", nil)); d.Name != "Toto" {
		t.Errorf("artist by ID: %+v", d)
	}
	genres := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Genres?ParentId="+musicID+"&IncludeItemTypes=MusicAlbum&Fields=ItemCounts", "", nil))
	if names(genres.Items) != "Rock" || *genres.Items[0].AlbumCount != 1 {
		t.Errorf("genres: %+v", genres.Items)
	}
	// Moonfin marks albums and artists as favourites through the API.
	if rec := c.do("POST", "/UserFavoriteItems/"+iv.ID, "", nil); rec.Code != http.StatusOK {
		t.Errorf("favourite album: %d", rec.Code)
	}
	if rec := c.do("POST", "/UserFavoriteItems/"+artist, "", nil); rec.Code != http.StatusOK {
		t.Errorf("favourite artist: %d", rec.Code)
	}
	if got := names(list("IncludeItemTypes=MusicAlbum&Recursive=true&IsFavorite=true").Items); got != "IV" {
		t.Errorf("favourite albums: %s", got)
	}
	if rec := c.do("POST", "/UserPlayedItems/"+iv.ID, "", nil); rec.Code != http.StatusOK {
		t.Errorf("album played: %d", rec.Code)
	}
	if got := names(list("ParentId=" + iv.ID + "&IncludeItemTypes=Audio&Filters=IsPlayed").Items); got != "Rosanna, Africa" {
		t.Errorf("tracks of a played album: %s", got)
	}
	var latest []BaseItemDto
	json.Unmarshal(c.do("GET", "/Users/"+uid+"/Items/Latest?ParentId="+musicID+"&Limit=5", "", nil).Body.Bytes(), &latest)
	if len(latest) != 2 || latest[0].Type != "MusicAlbum" {
		t.Errorf("latest: %+v", latest)
	}
	// Books: the library lists them recursively, and a book's "siblings"
	// (its parent without Recursive) are none.
	books := list("ParentId=" + booksID + "&IncludeItemTypes=Book,AudioBook&Recursive=true&Fields=People,Chapters")
	if names(books.Items) != "Blackout" || books.Items[0].Type != "AudioBook" || books.Items[0].MediaType != "Audio" ||
		len(books.Items[0].Chapters) != 2 || books.Items[0].People[0].Type != "Author" {
		t.Errorf("books: %+v", books.Items)
	}
	if n := len(list("ParentId=" + booksID + "&IncludeItemTypes=Audio,AudioBook").Items); n != 0 {
		t.Errorf("siblings of a book: %d", n)
	}
	_ = s
}

// moonfinProfile is the audio part of Moonfin's native device profile.
const moonfinProfile = `{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Audio",
	"Container":"aac,ac3,alac,ape,dts,eac3,flac,m4a,m4b,mka,mp3,oga,ogg,opus,wav,wma",
	"AudioCodec":"aac,ac3,alac,flac,mp2,mp3,opus,pcm_s16le,vorbis"}],
	"TranscodingProfiles":[{"Type":"Audio","Container":"ts","Protocol":"hls","AudioCodec":"aac"}]}}`

func TestAudioPlayback(t *testing.T) {
	s, c, uid := musicServer(t)
	info := func(id string) MediaSourceInfo {
		t.Helper()
		resp := decode[PlaybackInfoResponse](t, c.do("POST", "/Items/"+id+"/PlaybackInfo?UserId="+uid, moonfinProfile, nil))
		if len(resp.MediaSources) != 1 {
			t.Fatalf("sources: %+v", resp)
		}
		return resp.MediaSources[0]
	}
	mp3 := info("a1000000000000000000000000000002")
	if !mp3.SupportsDirectPlay || mp3.SupportsTranscoding || !strings.HasPrefix(mp3.DirectStreamURL, "/Audio/") ||
		mp3.MediaStreams[0].NormalizationGain == nil || *mp3.MediaStreams[0].NormalizationGain != -6.5 {
		t.Errorf("mp3: %+v", mp3)
	}
	wma := info("a1000000000000000000000000000003")
	if wma.SupportsDirectPlay || !wma.SupportsTranscoding || wma.TranscodingContainer != "ts" ||
		!strings.HasPrefix(wma.TranscodingURL, "/Audio/a1000000000000000000000000000003/main.m3u8?") ||
		!strings.Contains(wma.TranscodingURL, "ApiKey="+c.token) {
		t.Errorf("wma: %+v", wma)
	}
	// The playlist names segments that carry the token as well.
	rec := c.do("GET", wma.TranscodingURL, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hls/0.ts?ApiKey="+c.token) {
		t.Errorf("playlist %d: %s", rec.Code, rec.Body.String())
	}
	// Without conversion rights the WMA track gets no conversion.
	ctx := context.Background()
	if err := s.Store.SetConversion(ctx, uid, store.ConversionNone); err != nil {
		t.Fatal(err)
	}
	if w := info("a1000000000000000000000000000003"); w.SupportsTranscoding {
		t.Errorf("converted for a user without the right: %+v", w)
	}
	if rec := c.do("GET", wma.TranscodingURL, "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("playlist without the right: %d", rec.Code)
	}
	// Direct streams are the files.
	rec = c.do("GET", "/Audio/a1000000000000000000000000000002/stream?Static=true", "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "audio/mpeg" || rec.Body.String() != "audio data of 01 Rosanna.mp3" {
		t.Errorf("stream %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	// /Audio/universal plays what the client lists, else redirects to the
	// conversion.
	if err := s.Store.SetConversion(ctx, uid, store.ConversionAll); err != nil {
		t.Fatal(err)
	}
	rec = c.do("GET", "/Audio/a1000000000000000000000000000003/universal?Container=mp3,flac&AudioCodec=aac&TranscodingProtocol=hls", "", nil)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/main.m3u8?") {
		t.Errorf("universal wma: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = c.do("GET", "/Audio/a1000000000000000000000000000002/universal?Container=mp3,flac", "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("universal mp3: %d", rec.Code)
	}
}

func TestPlaylistsLyricsMixes(t *testing.T) {
	s, c, uid := musicServer(t)
	africa, rosanna, theme := "a1000000000000000000000000000001", "a1000000000000000000000000000002", "a1000000000000000000000000000003"
	rec := c.do("POST", "/Playlists", `{"Name":"Road trip","Ids":["`+africa+`","`+rosanna+`"],"IsPublic":false}`, nil)
	created := decode[map[string]string](t, rec)
	id := created["Id"]
	if rec.Code != http.StatusOK || id == "" {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.do("POST", "/Playlists/"+id+"/Items?Ids="+theme+","+africa+"&UserId="+uid, "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("add %d", rec.Code)
	}
	entries := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Playlists/"+id+"/Items?UserId="+uid+"&Fields=PlaylistItemId", "", nil))
	if names(entries.Items) != "Africa, Rosanna, Theme, Africa" || entries.Items[3].PlaylistItemID == entries.Items[0].PlaylistItemID {
		t.Fatalf("entries: %s", names(entries.Items))
	}
	// The second Africa moves to the front, the first is removed.
	c.do("POST", "/Playlists/"+id+"/Items/"+entries.Items[3].PlaylistItemID+"/Move/0", "", nil)
	c.do("DELETE", "/Playlists/"+id+"/Items?EntryIds="+entries.Items[0].PlaylistItemID, "", nil)
	c.do("POST", "/Playlists/"+id, `{"Name":"Long road trip"}`, nil)
	after := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Users/"+uid+"/Items?ParentId="+id, "", nil))
	if names(after.Items) != "Africa, Rosanna, Theme" {
		t.Errorf("after move and remove: %s", names(after.Items))
	}
	list := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Users/"+uid+"/Items?IncludeItemTypes=Playlist&Recursive=true", "", nil))
	if names(list.Items) != "Long road trip" || *list.Items[0].ChildCount != 3 || list.Items[0].MediaType != "Audio" {
		t.Errorf("playlists: %+v", list.Items)
	}
	// Another user sees nothing of it.
	other := &apiClient{t: t, h: c.h}
	addUser(t, s, "bert", "pw", false)
	login(t, other, "bert", "pw")
	if rec := other.do("GET", "/Playlists/"+id+"/Items", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("other user: %d", rec.Code)
	}
	if rec := c.do("DELETE", "/Items/"+id, "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete %d", rec.Code)
	}

	lyr := decode[LyricDto](t, c.do("GET", "/Audio/"+rosanna+"/Lyrics", "", nil))
	if len(lyr.Lyrics) != 2 || *lyr.Lyrics[1].Start != ticks(3500*time.Millisecond) || lyr.Metadata["IsSynced"] != true {
		t.Errorf("lyrics: %+v", lyr)
	}
	if rec := c.do("GET", "/Audio/"+africa+"/Lyrics", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("no lyrics: %d", rec.Code)
	}
	mix := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items/"+rosanna+"/InstantMix?Limit=25", "", nil))
	if names(mix.Items) != "Africa" {
		t.Errorf("mix: %s", names(mix.Items))
	}

	if rec := c.do("POST", "/Moonfin/Bookmarks/b1000000000000000000000000000001", `[{"positionMs":1000,"label":"x"}]`, nil); rec.Code != http.StatusNoContent {
		t.Errorf("bookmark %d", rec.Code)
	}
	bm := decode[map[string]json.RawMessage](t, c.do("GET", "/Moonfin/Bookmarks/b1000000000000000000000000000001", "", nil))
	if string(bm["bookmarks"]) != `[{"positionMs":1000,"label":"x"}]` || string(bm["notes"]) != "[]" {
		t.Errorf("bookmarks: %s %s", bm["bookmarks"], bm["notes"])
	}
}
