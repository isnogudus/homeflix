package jellyfin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
)

const (
	movieA = "a1000000000000000000000000000001" // one file
	movieB = "b1000000000000000000000000000001" // HD version, and an SD version in two parts
	fileA  = "fa1"
	fileHD = "fbhd"
	fileS1 = "fbsd1"
	fileS2 = "fbsd2"
)

// playbackServer serves two movies backed by real files whose probe data
// is that of "12 Years a Slave" (h264, AAC, about 2 h 9 min).
func playbackServer(t *testing.T) (*Server, *apiClient, map[string][]byte) {
	t.Helper()
	s := newTestServer(t, nil)
	dir := t.TempDir()
	probe := fixture(t, "twelve-years")
	contents := map[string][]byte{}
	file := func(id, item, version string, part int, name string) store.MediaFile {
		path := filepath.Join(dir, name)
		data := []byte(strings.Repeat(id+":", 100))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		contents[id] = data
		return store.MediaFile{ID: id, ItemID: item, Version: version, Part: part, Path: path, Size: int64(len(data)), Probe: probe}
	}
	items := []store.Item{
		{ID: movieA, LibraryID: libID, Type: "Movie", Title: "Alpha", SortTitle: "alpha", Year: 2001,
			DateCreated: time.Now().Add(-time.Hour), Files: []store.MediaFile{file(fileA, movieA, "", 0, "Alpha.mp4")}},
		{ID: movieB, LibraryID: libID, Type: "Movie", Title: "Beta", SortTitle: "beta", Year: 2002, DateCreated: time.Now(),
			Files: []store.MediaFile{
				file(fileHD, movieB, "HD", 0, "Beta [HD].mp4"),
				file(fileS1, movieB, "SD", 1, "Beta [SD] part1.mp4"),
				file(fileS2, movieB, "SD", 2, "Beta [SD] part2.mp4"),
			}},
	}
	s.Catalog.Locale = "de"
	s.Catalog.Set(catalog.Build([]store.Library{{ID: libID, Name: "Filme", CollectionType: "movies"}}, items, catalog.Options{Locale: "de"}))
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")
	return s, c, contents
}

func moonfinRequest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "profile", "testdata", "moonfin-2.7-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPlaybackInfo(t *testing.T) {
	_, c, _ := playbackServer(t)
	rec := c.do("POST", "/Items/"+movieB+"/PlaybackInfo?UserId=x", moonfinRequest(t), nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	info := decode[PlaybackInfoResponse](t, rec)
	if len(info.PlaySessionID) != 32 || len(info.MediaSources) != 2 {
		t.Fatalf("response: %+v", info)
	}
	hd, sd := info.MediaSources[0], info.MediaSources[1]
	if hd.ID != movieB || hd.Name != "HD" || sd.ID != fileS1 || sd.Name != "SD" {
		t.Errorf("sources: %s/%s, %s/%s", hd.ID, hd.Name, sd.ID, sd.Name)
	}
	if !hd.SupportsDirectPlay || !hd.SupportsDirectStream || hd.SupportsTranscoding ||
		!strings.Contains(hd.DirectStreamURL, "/Videos/"+movieB+"/stream?Static=true&MediaSourceId="+movieB) {
		t.Errorf("direct play: %+v", hd)
	}
	if *hd.DefaultAudioStreamIndex != 2 || *hd.DefaultSubtitleStreamIndex != -1 {
		t.Errorf("requested streams not echoed: audio %d, subtitle %d", *hd.DefaultAudioStreamIndex, *hd.DefaultSubtitleStreamIndex)
	}

	picked := decode[PlaybackInfoResponse](t, c.do("POST", "/Items/"+movieB+"/PlaybackInfo",
		`{"MediaSourceId":"`+fileS1+`"}`, nil))
	if len(picked.MediaSources) != 1 || picked.MediaSources[0].ID != fileS1 {
		t.Errorf("picked source: %+v", picked.MediaSources)
	}
	part := decode[PlaybackInfoResponse](t, c.do("POST", "/Items/"+fileS2+"/PlaybackInfo", "", nil))
	if len(part.MediaSources) != 1 || part.MediaSources[0].ID != fileS2 {
		t.Errorf("part source: %+v", part.MediaSources)
	}

	noMP4 := `{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mkv"}]}}`
	refused := decode[PlaybackInfoResponse](t, c.do("POST", "/Items/"+movieA+"/PlaybackInfo", noMP4, nil))
	if len(refused.MediaSources) != 1 || refused.MediaSources[0].SupportsDirectPlay || refused.MediaSources[0].DirectStreamURL != "" {
		t.Errorf("profile without mp4: %+v", refused.MediaSources)
	}
	if rec := c.do("POST", "/Items/ffffffffffffffffffffffffffffffff/PlaybackInfo", "", nil); rec.Code != 404 {
		t.Errorf("unknown item: %d", rec.Code)
	}
}

func TestStreaming(t *testing.T) {
	s, c, contents := playbackServer(t)
	token := c.token
	h := c.h
	get := func(method, path string, header http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range header {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	auth := "&ApiKey=" + token

	rec := get("GET", "/Videos/"+movieA+"/stream?Static=true&MediaSourceId="+movieA+auth, nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "video/mp4" || rec.Body.String() != string(contents[fileA]) {
		t.Errorf("full stream: %d %s, %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	rec = get("GET", "/Videos/"+movieA+"/stream.mp4?Static=true"+auth, http.Header{"Range": {"bytes=4-11"}})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != string(contents[fileA][4:12]) ||
		rec.Header().Get("Content-Range") != fmt.Sprintf("bytes 4-11/%d", len(contents[fileA])) {
		t.Errorf("range: %d %q %s", rec.Code, rec.Body.String(), rec.Header().Get("Content-Range"))
	}
	rec = get("HEAD", "/Videos/"+movieA+"/stream?Static=true"+auth, nil)
	if rec.Code != 200 || rec.Header().Get("Content-Length") != fmt.Sprint(len(contents[fileA])) || rec.Body.Len() != 0 {
		t.Errorf("head: %d %v", rec.Code, rec.Header())
	}
	for id, want := range map[string]string{
		"?Static=true&MediaSourceId=" + fileS1: fileS1,
		"?Static=true&MediaSourceId=" + fileS2: fileS2,
		"?Static=true":                         fileHD,
	} {
		if rec := get("GET", "/Videos/"+movieB+"/stream"+id+auth, nil); rec.Body.String() != string(contents[want]) {
			t.Errorf("stream %s served the wrong file", id)
		}
	}
	if rec := get("GET", "/Videos/"+fileS2+"/stream?Static=true"+auth, nil); rec.Body.String() != string(contents[fileS2]) {
		t.Error("stream by part ID served the wrong file")
	}
	if rec := get("GET", "/Videos/"+movieA+"/stream?Static=true", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("stream without token: %d", rec.Code)
	}
	// Downloads need the right.
	if rec := get("GET", "/Items/"+movieA+"/Download?ApiKey="+token, nil); rec.Code != http.StatusForbidden {
		t.Errorf("download without the right: %d", rec.Code)
	}
	if item := decode[BaseItemDto](t, c.do("GET", "/Items/"+movieA, "", nil)); item.CanDownload {
		t.Error("CanDownload without the right")
	}
	anna, _ := s.Store.UserByName(context.Background(), "anna")
	if err := s.Store.SetDownload(context.Background(), anna.ID, true); err != nil {
		t.Fatal(err)
	}
	rec = get("GET", "/Items/"+movieA+"/Download?ApiKey="+token, nil)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "Alpha.mp4") {
		t.Errorf("download: %d %v", rec.Code, rec.Header())
	}
	if item := decode[BaseItemDto](t, c.do("GET", "/Items/"+movieA, "", nil)); !item.CanDownload {
		t.Error("CanDownload missing with the right")
	}
}

func TestAdditionalParts(t *testing.T) {
	_, c, _ := playbackServer(t)
	beta := decode[BaseItemDto](t, c.do("GET", "/Items/"+movieB, "", nil))
	if beta.PartCount != 2 || beta.MediaSourceCount != 2 {
		t.Errorf("Beta: PartCount %d, MediaSourceCount %d", beta.PartCount, beta.MediaSourceCount)
	}
	parts := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Videos/"+fileS1+"/AdditionalParts", "", nil))
	if len(parts.Items) != 1 || parts.Items[0].ID != fileS2 || parts.Items[0].Name != "Beta – Teil 2" {
		t.Fatalf("parts of the SD version: %+v", parts.Items)
	}
	if len(parts.Items[0].MediaSources) != 1 || parts.Items[0].MediaSources[0].ID != fileS2 || parts.Items[0].ImageTags["Primary"] == "" {
		t.Errorf("part item: %+v", parts.Items[0])
	}
	if hd := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Videos/"+movieB+"/AdditionalParts", "", nil)); len(hd.Items) != 0 {
		t.Errorf("parts of the single-file HD version: %+v", hd.Items)
	}
	if item := decode[BaseItemDto](t, c.do("GET", "/Items/"+fileS2, "", nil)); item.ID != fileS2 || item.Type != "Movie" {
		t.Errorf("part by ID: %+v", item)
	}
}

func report(t *testing.T, c *apiClient, path, item, source string, frac float64) {
	t.Helper()
	runtime := int64(77193463330)
	body := fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PositionTicks":%d,"PlaySessionId":"p1"}`, item, source, int64(frac*float64(runtime)))
	if rec := c.do("POST", path, body, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
}

func userData(t *testing.T, c *apiClient, id string) UserItemDataDto {
	t.Helper()
	return decode[UserItemDataDto](t, c.do("GET", "/UserItems/"+id+"/UserData", "", nil))
}

func TestPlaybackProgress(t *testing.T) {
	_, c, _ := playbackServer(t)
	report(t, c, "/Sessions/Playing", movieA, movieA, 0)
	if d := userData(t, c, movieA); d.LastPlayedDate == nil || d.PlaybackPositionTicks != 0 {
		t.Errorf("after start: %+v", d)
	}
	report(t, c, "/Sessions/Playing/Progress", movieA, movieA, 0.5)
	if d := userData(t, c, movieA); d.PlaybackPositionTicks == 0 || d.PlayedPercentage < 49 || d.PlayedPercentage > 51 || d.Played {
		t.Errorf("at half: %+v", d)
	}
	resume := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/UserItems/Resume", "", nil))
	if names(resume.Items) != "Alpha" || resume.Items[0].UserData.PlaybackPositionTicks == 0 {
		t.Errorf("resume: %+v", resume.Items)
	}
	report(t, c, "/Sessions/Playing/Progress", movieA, movieA, 0.02)
	if d := userData(t, c, movieA); d.PlaybackPositionTicks != 0 {
		t.Errorf("below 5 %%: position %d kept", d.PlaybackPositionTicks)
	}
	report(t, c, "/Sessions/Playing/Stopped", movieA, movieA, 0.95)
	d := userData(t, c, movieA)
	if !d.Played || d.PlayCount != 1 || d.PlaybackPositionTicks != 0 {
		t.Errorf("stopped at 95 %%: %+v", d)
	}
	if r := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/UserItems/Resume", "", nil)); len(r.Items) != 0 {
		t.Errorf("finished movie still resumable: %s", names(r.Items))
	}
	// Latest hides played movies, as HidePlayedInLatest defaults to true.
	if latest := decode[[]BaseItemDto](t, c.do("GET", "/Items/Latest?ParentId="+libID, "", nil)); names(latest) != "Beta" {
		t.Errorf("latest: %s", names(latest))
	}
	played := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?Recursive=true&Filters=IsPlayed", "", nil))
	unplayed := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?Recursive=true&IsPlayed=false", "", nil))
	if names(played.Items) != "Alpha" || names(unplayed.Items) != "Beta" {
		t.Errorf("played %q, unplayed %q", names(played.Items), names(unplayed.Items))
	}
	if rec := c.do("POST", "/Sessions/Playing/Progress", `{"ItemId":"ffffffffffffffffffffffffffffffff","PositionTicks":5}`, nil); rec.Code != http.StatusNoContent {
		t.Errorf("report for unknown item: %d", rec.Code)
	}
}

func TestLastPartMarksMoviePlayed(t *testing.T) {
	_, c, _ := playbackServer(t)
	report(t, c, "/Sessions/Playing/Progress", fileS2, fileS2, 0.3)
	resume := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/UserItems/Resume", "", nil))
	if len(resume.Items) != 1 || resume.Items[0].ID != fileS2 {
		t.Errorf("resume lists %+v, want the second part", resume.Items)
	}
	report(t, c, "/Sessions/Playing/Stopped", fileS2, fileS2, 0.99)
	if d := userData(t, c, movieB); !d.Played {
		t.Errorf("movie not played after its last part: %+v", d)
	}
}

func TestPlayedAndFavorites(t *testing.T) {
	_, c, _ := playbackServer(t)
	d := decode[UserItemDataDto](t, c.do("POST", "/UserPlayedItems/"+movieB, "", nil))
	if !d.Played || d.PlayCount != 1 || d.ItemID != movieB {
		t.Errorf("mark played: %+v", d)
	}
	if part := userData(t, c, fileS2); !part.Played {
		t.Error("later part not marked played with its movie")
	}
	if rec := c.do("DELETE", "/Users/ffffffffffffffffffffffffffffffff/PlayedItems/"+movieB, "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("legacy path for another user as non-admin: %d", rec.Code)
	}
	d = decode[UserItemDataDto](t, c.do("DELETE", "/UserPlayedItems/"+movieB, "", nil))
	if d.Played || d.PlayCount != 0 || d.LastPlayedDate != nil {
		t.Errorf("mark unplayed: %+v", d)
	}
	if d := decode[UserItemDataDto](t, c.do("POST", "/UserFavoriteItems/"+movieA, "", nil)); !d.IsFavorite {
		t.Errorf("favorite: %+v", d)
	}
	favs := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?Recursive=true&Filters=IsFavorite", "", nil))
	if names(favs.Items) != "Alpha" || !favs.Items[0].UserData.IsFavorite {
		t.Errorf("favorites: %+v", favs.Items)
	}
	d = decode[UserItemDataDto](t, c.do("POST", "/UserItems/"+movieA+"/UserData", `{"PlaybackPositionTicks":600000000000}`, nil))
	if d.PlaybackPositionTicks != 600000000000 || !d.IsFavorite {
		t.Errorf("update user data: %+v", d)
	}
	if rec := c.do("POST", "/UserFavoriteItems/ffffffffffffffffffffffffffffffff", "", nil); rec.Code != 404 {
		t.Errorf("unknown item: %d", rec.Code)
	}
	if b, _ := io.ReadAll(c.do("POST", "/UserItems/"+movieA+"/Rating?Likes=true", "", nil).Body); !strings.Contains(string(b), `"IsFavorite":true`) {
		t.Errorf("rating: %s", b)
	}
}
