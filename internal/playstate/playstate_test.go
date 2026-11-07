package playstate

import (
	"context"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
)

func TestAudioRules(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := st.CreateUser(ctx, "anna", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	report := func(it Item, kind Kind, pos time.Duration) store.UserData {
		t.Helper()
		d, err := Report(ctx, st, u.ID, it, kind, &pos, false)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	song := Item{ID: "song", Movie: &catalog.Movie{Track: &catalog.Track{}}, Runtime: 4 * time.Minute}
	if d := report(song, Stop, 5*time.Second); d.Played {
		t.Error("a song stopped at 2 % is played")
	}
	if d := report(song, Stop, 30*time.Second); !d.Played || d.PlayCount != 1 || d.Position != 0 {
		t.Errorf("song after 30 s: %+v", d)
	}
	book := Item{ID: "book", Movie: &catalog.Movie{Book: &catalog.Book{}}, Runtime: 10 * time.Hour}
	if d := report(book, Progress, 3*time.Minute); d.Position != 0 {
		t.Errorf("book in the first minutes keeps %v", d.Position)
	}
	if d := report(book, Progress, 9*time.Hour); d.Position != 9*time.Hour || d.Played {
		t.Errorf("book at 9 h: %+v", d)
	}
	// Moonfin never stops a book: progress near the end finishes it, once.
	if d := report(book, Progress, 10*time.Hour-2*time.Minute); !d.Played || d.PlayCount != 1 || d.Position != 0 {
		t.Errorf("book near the end: %+v", d)
	}
	if d := report(book, Progress, 10*time.Hour-time.Minute); d.PlayCount != 1 {
		t.Errorf("finished twice: %+v", d)
	}
	if d := report(book, Stop, 10*time.Hour-30*time.Second); d.PlayCount != 1 {
		t.Errorf("the stop after the end counted again: %+v", d)
	}
}

func TestResolveBookRuntime(t *testing.T) {
	part := func(id string, d time.Duration) store.MediaFile {
		return store.MediaFile{ID: id, ItemID: "b", Part: 1, Path: "/b/" + id + ".mp3", Duration: d,
			Probe: `{"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3"}],"format":{"format_name":"mp3"}}`}
	}
	snap := catalog.Build([]store.Library{{ID: "l", CollectionType: "books"}}, []store.Item{{ID: "b", LibraryID: "l",
		Type: catalog.TypeAudioBook, Path: "/b", Files: []store.MediaFile{part("p1", 2*time.Minute), part("p2", 3*time.Minute)}}},
		catalog.Options{})
	it, ok := Resolve(snap, "b", "b")
	if !ok || it.Runtime != 5*time.Minute {
		t.Errorf("runtime %v", it.Runtime)
	}
}
