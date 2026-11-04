package catalog

import (
	"encoding/json"
	"testing"
	"time"

	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

// collectionSnapshot holds two Star Wars movies, a third movie of a series
// it alone is in, and one movie of no series.
func collectionSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	items := []store.Item{
		movie("sw5", "Das Imperium schlägt zurück", "imperium schlagt zuruck", 1980, day(1), file("f1", "", 0, 1080, false)),
		movie("sw4", "Krieg der Sterne", "krieg der sterne", 1977, day(5), file("f2", "", 0, 1080, false)),
		movie("heat", "Heat", "heat", 1995, day(2), file("f3", "", 0, 1080, true)),
		movie("abyss", "The Abyss", "abyss", 1989, day(3), file("f4", "", 0, 1080, false)),
	}
	row := func(id string, m tmdb.Movie) store.Metadata {
		b, _ := json.Marshal(m)
		return store.Metadata{ItemID: id, Status: store.MetaMatched, Data: string(b)}
	}
	sw := &tmdb.CollectionRef{ID: 10, Name: "Star Wars Collection"}
	meta := map[string]store.Metadata{
		"sw4":  row("sw4", tmdb.Movie{ID: 11, Title: "Krieg der Sterne", ReleaseDate: "1977-05-25", Collection: sw, Genres: []string{"Abenteuer"}}),
		"sw5":  row("sw5", tmdb.Movie{ID: 1891, Title: "Das Imperium schlägt zurück", ReleaseDate: "1980-05-20", Collection: sw}),
		"heat": row("heat", tmdb.Movie{ID: 949, Title: "Heat", ReleaseDate: "1995-12-15", Collection: &tmdb.CollectionRef{ID: 77, Name: "Heat"}}),
	}
	d, _ := json.Marshal(tmdb.Collection{ID: 10, Name: "Star Wars Filmreihe", Overview: "Eine Weltraumoper."})
	cols := map[int]store.Collection{10: {TMDbID: 10, Status: store.MetaMatched, Data: string(d)}}
	return Build([]store.Library{{ID: "lib", Name: "Filme", CollectionType: "movies"}}, items,
		Options{Locale: "en", Articles: []string{"the"}, Metadata: meta, Collections: cols})
}

func TestCollections(t *testing.T) {
	s := collectionSnapshot(t)
	cols := s.Collections()
	if len(cols) != 1 {
		t.Fatalf("collections: %d, want 1 (a series of one movie is none)", len(cols))
	}
	c := cols[0]
	if c.ID != CollectionID(10) || c.Name != "Star Wars Filmreihe" || c.Overview != "Eine Weltraumoper." ||
		titles(c.Movies) != "Krieg der Sterne, Das Imperium schlägt zurück" {
		t.Errorf("collection: %+v, movies %s", c, titles(c.Movies))
	}
	e := c.Entry()
	if e.Type != "BoxSet" || e.Set != c || e.Year != 1977 || !e.DateCreated.Equal(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("entry: %+v", e.Item)
	}
	if sw4, _ := s.Item("sw4"); sw4.Collection != c {
		t.Error("movie does not point to its collection")
	}
	if got, ok := s.Collection(c.ID); !ok || got != c {
		t.Error("lookup by ID")
	}
	if v, ok := s.CollectionsView(); !ok || v.ID != CollectionsViewID || v.Name != "Collections" ||
		v.CollectionType != "boxsets" || v.Movies != 1 {
		t.Errorf("collections view: %+v", v)
	}
	if _, ok := testSnapshot().CollectionsView(); ok {
		t.Error("collections view without collections")
	}

	name := []string{"SortName"}
	for what, tc := range map[string]struct {
		q    Query
		want string
	}{
		"movies only":       {Query{SortBy: name}, "The Abyss, Das Imperium schlägt zurück, Heat, Krieg der Sterne"},
		"collapsed":         {Query{Kinds: MoviesAndCollections, Collapse: true, SortBy: name}, "The Abyss, Heat, Star Wars Filmreihe"},
		"both, uncollapsed": {Query{Kinds: MoviesAndCollections, SortBy: name}, "The Abyss, Das Imperium schlägt zurück, Heat, Krieg der Sterne, Star Wars Filmreihe"},
		"collapsed, by name filter": {Query{Kinds: MoviesAndCollections, Collapse: true, NameStartsWith: "s"},
			"Star Wars Filmreihe"},
		"collapsed, newest first": {Query{Kinds: MoviesAndCollections, Collapse: true, SortBy: []string{"DateCreated"},
			Descending: []bool{true}}, "Star Wars Filmreihe, The Abyss, Heat"},
		// A movie filter turns grouping off, as in Jellyfin.
		"collapsed, genre filter": {Query{Kinds: MoviesAndCollections, Collapse: true, GenreIDs: []string{GenreID("Abenteuer")}},
			"Krieg der Sterne"},
		"collections only":        {Query{Kinds: CollectionsOnly, LibraryID: "lib"}, "Star Wars Filmreihe"},
		"collections, other lib":  {Query{Kinds: CollectionsOnly, LibraryID: "other"}, ""},
		"collections, genre":      {Query{Kinds: CollectionsOnly, GenreIDs: []string{GenreID("Abenteuer")}}, "Star Wars Filmreihe"},
		"collections, no match":   {Query{Kinds: CollectionsOnly, HasSubtitles: ptr(true)}, ""},
		"members, premiere order": {Query{CollectionID: c.ID}, "Krieg der Sterne, Das Imperium schlägt zurück"},
		"members, sorted by name": {Query{CollectionID: c.ID, SortBy: name}, "Das Imperium schlägt zurück, Krieg der Sterne"},
		"members, unknown":        {Query{CollectionID: "nope"}, ""},
	} {
		got, total := s.Movies(tc.q)
		if titles(got) != tc.want || total != len(got) {
			t.Errorf("%s: %q (%d), want %q", what, titles(got), total, tc.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }
