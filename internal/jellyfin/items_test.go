package jellyfin

import (
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/images"
	"homeflix/internal/store"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "media", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const libID = "11111111111111111111111111111111"

// libraryServer serves a catalog made from real probe data:
//
//	Arrival (2016)          one version, HDR10, DTS-HD MA, PGS
//	Dune (2021)             two versions (UHD Dolby Vision, 1080p h264), one extra
//	12 Years a Slave (2013) h264 with four reference frames
func libraryServer(t *testing.T) (*Server, *apiClient, *apiClient) {
	t.Helper()
	s := newTestServer(t, nil)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	f := func(id, item, version, path, probe string) store.MediaFile {
		return store.MediaFile{ID: id, ItemID: item, Version: version, Path: path, Size: 1 << 30, Probe: probe}
	}
	items := []store.Item{
		{ID: "a0000000000000000000000000000001", LibraryID: libID, Type: "Movie", Title: "Arrival", SortTitle: "arrival",
			Year: 2016, IMDbID: "tt2543164", DateCreated: day(1), Path: "/m/Arrival (2016).mkv",
			Files: []store.MediaFile{f("fa", "a0000000000000000000000000000001", "", "/m/Arrival (2016).mkv", fixture(t, "arrival"))}},
		{ID: "d0000000000000000000000000000001", LibraryID: libID, Type: "Movie", Title: "Dune", SortTitle: "dune",
			Year: 2021, DateCreated: day(3), Path: "/m/Dune (2021)",
			Files: []store.MediaFile{
				f("fd1", "d0000000000000000000000000000001", "UHD", "/m/Dune (2021)/Dune [UHD].mkv", fixture(t, "dune")),
				f("fd2", "d0000000000000000000000000000001", "HD", "/m/Dune (2021)/Dune [HD].mp4", fixture(t, "twelve-years")),
			}},
		{ID: "e0000000000000000000000000000001", LibraryID: libID, ParentID: "d0000000000000000000000000000001",
			Type: "Video", ExtraType: "BehindTheScenes", Title: "Making of", SortTitle: "making of", DateCreated: day(3),
			Files: []store.MediaFile{f("fe", "e0000000000000000000000000000001", "", "/m/Dune (2021)/behind the scenes/Making of.mkv", fixture(t, "twelve-years"))}},
		{ID: "c0000000000000000000000000000001", LibraryID: libID, Type: "Movie", Title: "12 Years a Slave",
			SortTitle: "00000012 years a slave", Year: 2013, DateCreated: day(2), Path: "/m/12 Years a Slave (2013).mp4",
			Files: []store.MediaFile{f("fc", "c0000000000000000000000000000001", "", "/m/12 Years a Slave (2013).mp4", fixture(t, "twelve-years"))}},
	}
	s.Catalog.Set(catalog.Build([]store.Library{{ID: libID, Name: "Filme", CollectionType: "movies"}}, items, catalog.Options{Locale: "en"}))
	h := s.Handler()
	addUser(t, s, "admin", "a", true)
	addUser(t, s, "anna", "b", false)
	admin, anna := &apiClient{t: t, h: h}, &apiClient{t: t, h: h}
	login(t, admin, "admin", "a")
	login(t, anna, "anna", "b")
	return s, admin, anna
}

func names(items []BaseItemDto) string {
	var n []string
	for _, it := range items {
		n = append(n, it.Name)
	}
	return strings.Join(n, ", ")
}

func TestViews(t *testing.T) {
	_, _, c := libraryServer(t)
	views := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/UserViews", "", nil))
	if len(views.Items) != 1 {
		t.Fatalf("views: %+v", views)
	}
	v := views.Items[0]
	if v.ID != libID || v.Type != "CollectionFolder" || v.CollectionType != "movies" || !v.IsFolder || v.ChildCount == nil || *v.ChildCount != 3 {
		t.Errorf("view: %+v", v)
	}
	// /Items without a parent lists the views, as the user's root folder.
	if root := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items", "", nil)); names(root.Items) != "Filme" {
		t.Errorf("root items: %s", names(root.Items))
	}
}

func TestItemListing(t *testing.T) {
	_, _, c := libraryServer(t)
	list := func(query string) QueryResult[BaseItemDto] {
		return decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?"+query, "", nil))
	}
	base := "ParentId=" + libID + "&IncludeItemTypes=Movie&Recursive=true"
	for query, want := range map[string]string{
		base + "&SortBy=SortName&SortOrder=Ascending":                 "12 Years a Slave, Arrival, Dune",
		base + "&SortBy=ProductionYear,SortName&SortOrder=Descending": "Dune, Arrival, 12 Years a Slave",
		base + "&SortBy=DateCreated&SortOrder=Descending":             "Dune, 12 Years a Slave, Arrival",
		base + "&SortBy=SortName&StartIndex=1&Limit=1":                "Arrival",
		"Recursive=true&SearchTerm=dun":                               "Dune",
		base + "&NameStartsWith=A":                                    "Arrival",
		base + "&NameLessThan=A":                                      "12 Years a Slave",
		base + "&Years=2013,2021&SortBy=SortName":                     "12 Years a Slave, Dune",
		base + "&Is4K=true&SortBy=SortName":                           "Arrival, Dune",
		base + "&HasSpecialFeature=true":                              "Dune",
		"Recursive=true&AnyProviderIdEquals=imdb.tt2543164":           "Arrival",
		"Recursive=true&IncludeItemTypes=Series":                      "",
		base + "&Filters=IsFavorite":                                  "",
		base + "&Genres=Drama":                                        "",
		"ParentId=ffffffffffffffffffffffffffffffff&Recursive=true":    "",
	} {
		if got := names(list(query).Items); got != want {
			t.Errorf("%s:\n got %q\nwant %q", query, got, want)
		}
	}
	page := list(base + "&SortBy=SortName&Limit=2")
	if page.TotalRecordCount != 3 || len(page.Items) != 2 {
		t.Errorf("paging: total %d, %d items", page.TotalRecordCount, len(page.Items))
	}
	if page.Items[0].MediaSources != nil || page.Items[0].MediaStreams != nil || page.Items[0].Path != "" {
		t.Error("list without Fields carries media sources, streams or path")
	}
	withFields := list(base + "&SortBy=SortName&Fields=MediaSources,MediaStreams,Path")
	if len(withFields.Items[0].MediaSources) != 1 || len(withFields.Items[0].MediaStreams) == 0 {
		t.Error("Fields=MediaSources,MediaStreams not honoured")
	}
	if withFields.Items[0].Path != "" {
		t.Error("non-admin sees the file path")
	}
	ids := list("Ids=" + libID + ",a0000000-0000-0000-0000-000000000001")
	if names(ids.Items) != "Filme, Arrival" {
		t.Errorf("Ids: %s", names(ids.Items))
	}
}

func TestItemDetails(t *testing.T) {
	_, admin, anna := libraryServer(t)
	arrival := decode[BaseItemDto](t, anna.do("GET", "/Items/a0000000000000000000000000000001", "", nil))
	if arrival.Type != "Movie" || arrival.ProductionYear != 2016 || arrival.ProviderIDs["Imdb"] != "tt2543164" ||
		arrival.Container != "mkv" || !arrival.HasSubtitles || arrival.RunTimeTicks != 69920000000 ||
		len(arrival.Chapters) != 16 || arrival.ParentID != libID {
		t.Errorf("arrival: type %s year %d ids %v container %s subs %v ticks %d chapters %d parent %s",
			arrival.Type, arrival.ProductionYear, arrival.ProviderIDs, arrival.Container, arrival.HasSubtitles,
			arrival.RunTimeTicks, len(arrival.Chapters), arrival.ParentID)
	}
	if len(arrival.MediaSources) != 1 || arrival.MediaSources[0].ID != arrival.ID || arrival.MediaSources[0].Path != "" {
		t.Fatalf("arrival sources: %+v", arrival.MediaSources)
	}
	src := arrival.MediaSources[0]
	if *src.DefaultAudioStreamIndex != 1 || *src.DefaultSubtitleStreamIndex != 3 || !src.SupportsDirectPlay || src.SupportsTranscoding {
		t.Errorf("source defaults: %+v", src)
	}
	var subs, audio int
	for _, st := range src.MediaStreams {
		switch st.Type {
		case "Subtitle":
			subs++
		case "Audio":
			audio++
		}
	}
	if subs != 6 || audio != 2 {
		t.Errorf("details list %d audio and %d subtitle streams, want 2 and 6", audio, subs)
	}
	if arrival.ImageTags["Primary"] == "" || len(arrival.BackdropImageTags) != 1 || arrival.PrimaryImageAspectRatio < 0.66 || arrival.PrimaryImageAspectRatio > 0.67 {
		t.Errorf("images: %v %v %v", arrival.ImageTags, arrival.BackdropImageTags, arrival.PrimaryImageAspectRatio)
	}

	dune := decode[BaseItemDto](t, admin.do("GET", "/Items/d0000000000000000000000000000001", "", nil))
	if dune.MediaSourceCount != 2 || len(dune.MediaSources) != 2 || dune.SpecialFeatureCount != 1 {
		t.Fatalf("dune: %d sources, count %d, extras %d", len(dune.MediaSources), dune.MediaSourceCount, dune.SpecialFeatureCount)
	}
	uhd, hd := dune.MediaSources[0], dune.MediaSources[1]
	if uhd.Name != "UHD" || uhd.ID != dune.ID || hd.Name != "HD" || hd.ID != "fd2" {
		t.Errorf("versions: %s/%s, %s/%s", uhd.Name, uhd.ID, hd.Name, hd.ID)
	}
	if uhd.Path != "/m/Dune (2021)/Dune [UHD].mkv" || dune.Path != "/m/Dune (2021)" {
		t.Errorf("admin paths: source %q, item %q", uhd.Path, dune.Path)
	}
	v := uhd.MediaStreams[0]
	if v.VideoRangeType != "DOVIWithEL" || v.DvProfile == nil || *v.DvProfile != 7 || v.BitDepth != 10 {
		t.Errorf("dolby vision stream: %+v", v)
	}
	if h := hd.MediaStreams[0]; h.RefFrames != 4 || h.Codec != "h264" || h.IsAVC == nil || !*h.IsAVC {
		t.Errorf("h264 stream: refs %d codec %s", h.RefFrames, h.Codec)
	}
	if rec := anna.do("GET", "/Users/ffffffffffffffffffffffffffffffff/Items/"+libID, "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("another user's item path as non-admin: %d", rec.Code)
	}
	if rec := anna.do("GET", "/Items/ffffffffffffffffffffffffffffffff", "", nil); rec.Code != 404 {
		t.Errorf("unknown item: %d", rec.Code)
	}
}

func TestLatestExtrasAncestors(t *testing.T) {
	_, _, c := libraryServer(t)
	latest := decode[[]BaseItemDto](t, c.do("GET", "/Items/Latest?ParentId="+libID+"&Limit=2", "", nil))
	if names(latest) != "Dune, 12 Years a Slave" {
		t.Errorf("latest: %s", names(latest))
	}
	extras := decode[[]BaseItemDto](t, c.do("GET", "/Items/d0000000000000000000000000000001/SpecialFeatures", "", nil))
	if names(extras) != "Making of" || extras[0].ExtraType != "BehindTheScenes" || extras[0].ParentID != "d0000000000000000000000000000001" {
		t.Errorf("extras: %+v", extras)
	}
	if extras[0].PrimaryImageAspectRatio < 1.7 || len(extras[0].BackdropImageTags) != 0 {
		t.Errorf("extra images: aspect %v, backdrops %v", extras[0].PrimaryImageAspectRatio, extras[0].BackdropImageTags)
	}
	trailers := decode[[]BaseItemDto](t, c.do("GET", "/Items/d0000000000000000000000000000001/LocalTrailers", "", nil))
	if len(trailers) != 0 {
		t.Errorf("trailers: %s", names(trailers))
	}
	anc := decode[[]BaseItemDto](t, c.do("GET", "/Items/e0000000000000000000000000000001/Ancestors", "", nil))
	if names(anc) != "Dune, Filme" {
		t.Errorf("ancestors: %s", names(anc))
	}
	filters := decode[map[string]any](t, c.do("GET", "/Items/Filters?ParentId="+libID, "", nil))
	if years, _ := filters["Years"].([]any); len(years) != 3 {
		t.Errorf("filters: %v", filters)
	}
}

func TestItemImages(t *testing.T) {
	s, _, _ := libraryServer(t)
	dir := t.TempDir()
	frame := filepath.Join(dir, "frame.png")
	pic := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for i := range pic.Pix {
		pic.Pix[i] = 128
	}
	fh, _ := os.Create(frame)
	png.Encode(fh, pic)
	fh.Close()
	bin := filepath.Join(dir, "ffmpeg")
	os.WriteFile(bin, []byte("#!/bin/sh\nfor a; do case $a in *.png) cp "+frame+" \"$a\";; esac; done\n"), 0o755)
	s.Images = &images.Service{FFmpeg: bin, Dir: filepath.Join(dir, "cache"), Workers: 1}
	h := s.Handler()

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	snap := s.Catalog.Snapshot()
	arrival, _ := snap.Item("a0000000000000000000000000000001")
	tag := images.Tag(arrival, images.Primary)
	rec := get("/Items/a0000000000000000000000000000001/Images/Primary?maxHeight=300&tag=" + tag)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("poster: %d %v", rec.Code, rec.Header())
	}
	img, err := jpeg.Decode(rec.Body)
	if err != nil || img.Bounds().Dx() != 200 || img.Bounds().Dy() != 300 {
		t.Errorf("poster %v, %v", img.Bounds(), err)
	}
	if rec := get("/Items/a0000000000000000000000000000001/Images/Backdrop/0?maxWidth=640"); rec.Code != 200 {
		t.Errorf("backdrop: %d", rec.Code)
	}
	for _, p := range []string{
		"/Items/a0000000000000000000000000000001/Images/Logo",
		"/Items/a0000000000000000000000000000001/Images/Backdrop/1",
		"/Items/e0000000000000000000000000000001/Images/Backdrop",
		"/Items/" + libID + "/Images/Primary",
		"/Items/ffffffffffffffffffffffffffffffff/Images/Primary",
	} {
		if rec := get(p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
	}
}
