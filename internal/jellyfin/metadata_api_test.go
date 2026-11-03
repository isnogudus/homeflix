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

func metaRow(t *testing.T, id string, m tmdb.Movie, poster string) store.Metadata {
	t.Helper()
	b, _ := json.Marshal(m)
	return store.Metadata{ItemID: id, Source: "search", Status: store.MetaMatched, TMDbID: m.ID, Data: string(b), Poster: poster}
}

// metadataServer serves three movies, two of them matched at TMDB.
func metadataServer(t *testing.T) (*Server, *apiClient) {
	t.Helper()
	s := newTestServer(t, nil)
	dir := t.TempDir()
	poster := "329865-poster-p.jpg"
	f, _ := os.Create(filepath.Join(dir, poster))
	jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 400, 600)), nil)
	f.Close()

	probe := fixture(t, "twelve-years")
	item := func(id, title string, year int) store.Item {
		return store.Item{ID: id, LibraryID: libID, Type: "Movie", Title: title, SortTitle: strings.ToLower(title), Year: year,
			DateCreated: time.Now(), Files: []store.MediaFile{{ID: "f" + id, ItemID: id, Path: "/m/" + id, Probe: probe}}}
	}
	items := []store.Item{
		item("a0000000000000000000000000000001", "Arrival", 2016),
		item("b0000000000000000000000000000001", "Panem", 2012),
		item("c0000000000000000000000000000001", "Unknown Film", 2001),
	}
	meta := map[string]store.Metadata{
		"a0000000000000000000000000000001": metaRow(t, "a0000000000000000000000000000001", tmdb.Movie{
			ID: 329865, IMDbID: "tt2543164", Title: "Arrival", OriginalTitle: "Arrival", Overview: "Außerirdische landen.",
			Tagline: "Warum sind sie hier?", Genres: []string{"Drama", "Science Fiction"}, Studios: []string{"FilmNation"},
			People: []tmdb.Person{{ID: 137427, Name: "Denis Villeneuve", Type: "Director"},
				{ID: 9273, Name: "Amy Adams", Role: "Louise Banks", Type: "Actor"}},
			ReleaseDate: "2016-11-24", Rating: 7.6, Certification: "FSK-12",
		}, poster),
		"b0000000000000000000000000000001": metaRow(t, "b0000000000000000000000000000001", tmdb.Movie{
			ID: 70160, Title: "Die Tribute von Panem - The Hunger Games", OriginalTitle: "The Hunger Games",
			Genres: []string{"Science Fiction", "Abenteuer"}, People: []tmdb.Person{{ID: 72129, Name: "Jennifer Lawrence", Type: "Actor"}},
			ReleaseDate: "2012-03-22", Certification: "FSK-12",
		}, ""),
		"c0000000000000000000000000000001": {ItemID: "c0000000000000000000000000000001", Status: store.MetaNotFound},
	}
	s.Catalog.Set(catalog.Build([]store.Library{{ID: libID, Name: "Filme", CollectionType: "movies"}}, items,
		catalog.Options{Locale: "de", Articles: []string{"die", "the"}, Metadata: meta, MetadataDir: dir}))
	s.Images = &images.Service{FFmpeg: "/nonexistent/ffmpeg", Dir: filepath.Join(dir, "cache"), Workers: 1}
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")
	return s, c
}

func TestMetadataInItems(t *testing.T) {
	_, c := metadataServer(t)
	a := decode[BaseItemDto](t, c.do("GET", "/Items/a0000000000000000000000000000001", "", nil))
	if a.Overview != "Außerirdische landen." || len(a.Taglines) != 1 || len(a.Genres) != 2 || len(a.GenreItems) != 2 ||
		a.OfficialRating != "FSK-12" || a.CommunityRating == nil || *a.CommunityRating != 7.6 ||
		a.PremiereDate == nil || a.PremiereDate.Year() != 2016 || a.ProviderIDs["Tmdb"] != "329865" {
		t.Errorf("arrival: %+v", a)
	}
	if len(a.People) != 2 || a.People[1].Role != "Louise Banks" || a.People[1].ID != catalog.PersonID(9273) || len(a.Studios) != 1 {
		t.Errorf("people/studios: %+v %+v", a.People, a.Studios)
	}
	b := decode[BaseItemDto](t, c.do("GET", "/Items/b0000000000000000000000000000001", "", nil))
	if b.Name != "Die Tribute von Panem - The Hunger Games" || b.OriginalTitle != "The Hunger Games" ||
		b.SortName != "tribute von panem the hunger games" {
		t.Errorf("TMDB title and sort name: %q %q %q", b.Name, b.OriginalTitle, b.SortName)
	}
	list := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?Recursive=true&SortBy=SortName", "", nil))
	if names(list.Items) != "Arrival, Die Tribute von Panem - The Hunger Games, Unknown Film" {
		t.Errorf("sorted by TMDB titles: %s", names(list.Items))
	}
	if list.Items[0].Overview != "" || len(list.Items[0].People) != 0 {
		t.Error("list without Fields carries overview or people")
	}
	withFields := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?Recursive=true&SortBy=SortName&Fields=Overview,People", "", nil))
	if withFields.Items[0].Overview == "" || len(withFields.Items[0].People) != 2 {
		t.Error("Fields=Overview,People not honoured")
	}
	if found := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?Recursive=true&SearchTerm=hunger%20games", "", nil)); names(found.Items) != "Die Tribute von Panem - The Hunger Games" {
		t.Errorf("search by TMDB title: %s", names(found.Items))
	}
}

func TestGenresPeopleSimilar(t *testing.T) {
	_, c := metadataServer(t)
	genres := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Genres?ParentId="+libID, "", nil))
	if names(genres.Items) != "Abenteuer, Drama, Science Fiction" || genres.Items[2].Type != "Genre" || *genres.Items[2].ChildCount != 2 {
		t.Errorf("genres: %+v", genres.Items)
	}
	for q, want := range map[string]string{
		"Genres=Drama": "Arrival",
		"GenreIds=" + catalog.GenreID("Abenteuer"):    "Die Tribute von Panem - The Hunger Games",
		"Genres=Science%20Fiction&SortBy=SortName":    "Arrival, Die Tribute von Panem - The Hunger Games",
		"PersonIds=" + catalog.PersonID(72129):        "Die Tribute von Panem - The Hunger Games",
		"StudioIds=" + catalog.StudioID("FilmNation"): "Arrival",
	} {
		got := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items?Recursive=true&"+q, "", nil))
		if names(got.Items) != want {
			t.Errorf("%s: %s, want %s", q, names(got.Items), want)
		}
	}
	person := decode[BaseItemDto](t, c.do("GET", "/Items/"+catalog.PersonID(9273), "", nil))
	if person.Type != "Person" || person.Name != "Amy Adams" {
		t.Errorf("person item: %+v", person)
	}
	if people := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Persons?SearchTerm=adams", "", nil)); names(people.Items) != "Amy Adams" {
		t.Errorf("person search: %s", names(people.Items))
	}
	similar := decode[QueryResult[BaseItemDto]](t, c.do("GET", "/Items/a0000000000000000000000000000001/Similar", "", nil))
	if names(similar.Items) != "Die Tribute von Panem - The Hunger Games" {
		t.Errorf("similar: %s", names(similar.Items))
	}
	filters := decode[map[string]any](t, c.do("GET", "/Items/Filters?ParentId="+libID, "", nil))
	if g, _ := filters["Genres"].([]any); len(g) != 3 {
		t.Errorf("filters genres: %v", filters)
	}
	if r, _ := filters["OfficialRatings"].([]any); len(r) != 1 || r[0] != "FSK-12" {
		t.Errorf("filters ratings: %v", filters)
	}
}

func TestPosterFromTMDB(t *testing.T) {
	s, c := metadataServer(t)
	a := decode[BaseItemDto](t, c.do("GET", "/Items/a0000000000000000000000000000001", "", nil))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/Items/a0000000000000000000000000000001/Images/Primary?maxHeight=300&tag="+a.ImageTags["Primary"], nil))
	if rec.Code != 200 {
		t.Fatalf("poster: %d", rec.Code)
	}
	if img, err := jpeg.Decode(rec.Body); err != nil || img.Bounds().Dx() != 200 || img.Bounds().Dy() != 300 {
		t.Errorf("poster %v, %v", img.Bounds(), err)
	}
}
