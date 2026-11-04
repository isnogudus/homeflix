package jellyfin

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/images"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

const (
	hungerID = "a1000000000000000000000000000002"
	fireID   = "a1000000000000000000000000000003"
	alphaID  = "a1000000000000000000000000000004"
)

// collectionServer serves two movies of the Panem series and one movie of
// no series.
func collectionServer(t *testing.T) (*Server, *apiClient, string) {
	t.Helper()
	s := newTestServer(t, nil)
	dir := t.TempDir()
	poster := "1234-collection-poster-p.jpg"
	f, _ := os.Create(filepath.Join(dir, poster))
	jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 400, 600)), nil)
	f.Close()

	probe := fixture(t, "twelve-years")
	item := func(id, title string, year int, added time.Time) store.Item {
		return store.Item{ID: id, LibraryID: libID, Type: "Movie", Title: title, SortTitle: strings.ToLower(title), Year: year,
			DateCreated: added, Files: []store.MediaFile{{ID: "f" + id, ItemID: id, Path: "/m/" + id, Probe: probe}}}
	}
	now := time.Now()
	items := []store.Item{
		item(fireID, "Catching Fire", 2013, now),
		item(hungerID, "The Hunger Games", 2012, now.Add(-time.Hour)),
		item(alphaID, "Alpha", 2001, now.Add(-2*time.Hour)),
	}
	panem := &tmdb.CollectionRef{ID: 1234, Name: "The Hunger Games Collection"}
	meta := map[string]store.Metadata{
		fireID:   metaRow(t, fireID, tmdb.Movie{ID: 101299, Title: "Catching Fire", ReleaseDate: "2013-11-21", Collection: panem}, ""),
		hungerID: metaRow(t, hungerID, tmdb.Movie{ID: 70160, Title: "The Hunger Games", ReleaseDate: "2012-03-12", Collection: panem}, ""),
	}
	d, _ := json.Marshal(tmdb.Collection{ID: 1234, Name: "Die Tribute von Panem Filmreihe", Overview: "Die Reihe."})
	cols := map[int]store.Collection{1234: {TMDbID: 1234, Status: store.MetaMatched, Data: string(d), Poster: poster}}
	s.Catalog.Set(catalog.Build([]store.Library{{ID: libID, Name: "Filme", CollectionType: "movies"}}, items,
		catalog.Options{Locale: "de", Articles: []string{"die", "the"}, Metadata: meta, Collections: cols, MetadataDir: dir}))
	s.Images = &images.Service{FFmpeg: "/nonexistent/ffmpeg", Dir: filepath.Join(dir, "cache"), Workers: 1}
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")
	return s, c, catalog.CollectionID(1234)
}

func TestCollectionsInItems(t *testing.T) {
	_, c, colID := collectionServer(t)
	list := func(q string) QueryResult[BaseItemDto] {
		t.Helper()
		return decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?"+q, "", nil))
	}
	// Moonfin's movie library with "group into collections".
	grouped := list("ParentId=" + libID + "&IncludeItemTypes=Movie,BoxSet&CollapseBoxSetItems=true&Recursive=true&SortBy=SortName")
	if names(grouped.Items) != "Alpha, Die Tribute von Panem Filmreihe" || grouped.TotalRecordCount != 2 {
		t.Fatalf("grouped: %s (%d)", names(grouped.Items), grouped.TotalRecordCount)
	}
	set := grouped.Items[1]
	if set.Type != "BoxSet" || set.ID != colID || !set.IsFolder || *set.ChildCount != 2 || set.ProductionYear != 2012 ||
		set.ImageTags["Primary"] == "" || set.ParentID != libID || *set.UserData.UnplayedItemCount != 2 {
		t.Errorf("box set: %+v", set)
	}
	// Without grouping, and with a filter that turns grouping off.
	if got := list("ParentId=" + libID + "&IncludeItemTypes=Movie&ExcludeItemTypes=BoxSet&Recursive=true"); len(got.Items) != 3 {
		t.Errorf("movies only: %s", names(got.Items))
	}
	if got := list("ParentId=" + libID + "&IncludeItemTypes=Movie,BoxSet&CollapseBoxSetItems=true&Recursive=true&Years=2013"); names(got.Items) != "Catching Fire" {
		t.Errorf("filtered: %s", names(got.Items))
	}
	// The collections row, and the scan Moonfin makes for a movie's series.
	for _, q := range []string{"IncludeItemTypes=BoxSet&Recursive=true", "ParentId=" + libID + "&IncludeItemTypes=BoxSet&Recursive=true&SortBy=SortName"} {
		if got := list(q); names(got.Items) != "Die Tribute von Panem Filmreihe" {
			t.Errorf("%s: %s", q, names(got.Items))
		}
	}
	// Its movies by premiere date, and its details.
	members := list("ParentId=" + colID + "&Fields=PrimaryImageAspectRatio,BasicSyncInfo,People")
	if names(members.Items) != "The Hunger Games, Catching Fire" || members.TotalRecordCount != 2 {
		t.Errorf("members: %s", names(members.Items))
	}
	details := decode[BaseItemDto](t, c.do("GET", "/Items/"+colID, "", nil))
	if details.Type != "BoxSet" || details.Overview != "Die Reihe." || details.ProviderIDs["Tmdb"] != "1234" {
		t.Errorf("details: %+v", details)
	}
	if anc := decode[[]BaseItemDto](t, c.do("GET", "/Items/"+colID+"/Ancestors", "", nil)); names(anc) != "Filme" {
		t.Errorf("ancestors: %s", names(anc))
	}
	if x := decode[[]BaseItemDto](t, c.do("GET", "/Items/"+colID+"/SpecialFeatures", "", nil)); len(x) != 0 {
		t.Errorf("special features: %v", x)
	}
}

func TestCollectionUserDataAndImages(t *testing.T) {
	s, c, colID := collectionServer(t)
	played := decode[UserItemDataDto](t, c.do("POST", "/UserPlayedItems/"+colID, "", nil))
	if !played.Played || *played.UnplayedItemCount != 0 || played.PlayedPercentage != 100 {
		t.Errorf("collection marked played: %+v", played)
	}
	for _, id := range []string{hungerID, fireID} {
		if d := userData(t, c, id); !d.Played {
			t.Errorf("movie %s not played", id)
		}
	}
	if d := userData(t, c, alphaID); d.Played {
		t.Error("movie outside the collection marked played")
	}
	fav := decode[UserItemDataDto](t, c.do("POST", "/UserFavoriteItems/"+colID, "", nil))
	if !fav.IsFavorite || !fav.Played {
		t.Errorf("favourite: %+v", fav)
	}
	if d := decode[BaseItemDto](t, c.do("GET", "/Items/"+colID, "", nil)); !d.UserData.IsFavorite || !d.UserData.Played {
		t.Errorf("user data in details: %+v", d.UserData)
	}
	decode[UserItemDataDto](t, c.do("DELETE", "/UserPlayedItems/"+colID, "", nil))
	if d := userData(t, c, hungerID); d.Played {
		t.Error("movie still played after unmarking the collection")
	}

	tag := decode[BaseItemDto](t, c.do("GET", "/Items/"+colID, "", nil)).ImageTags["Primary"]
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/Items/"+colID+"/Images/Primary?maxHeight=300&tag="+tag, nil))
	if rec.Code != 200 {
		t.Fatalf("poster: %d", rec.Code)
	}
	if img, err := jpeg.Decode(rec.Body); err != nil || img.Bounds().Dy() != 300 {
		t.Errorf("poster %v, %v", img.Bounds(), err)
	}
}
