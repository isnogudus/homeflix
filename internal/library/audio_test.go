package library

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWalkMusic(t *testing.T) {
	root := t.TempDir()
	touch(t, root,
		"Albums/ABBA/Gold/01 Dancing Queen.mp3", "Albums/ABBA/Gold/02 SOS.mp3", "Albums/ABBA/Gold/folder.jpg",
		"Albums/ABBA/Gold/album.nfo", "Albums/ABBA/artist.nfo", "Albums/ABBA/backdrop.jpg",
		"Albums/Toto/IV/Disc 01/01 Rosanna.flac", "Albums/Toto/IV/Disc 01/01 Rosanna.lrc",
		"Albums/Toto/IV/notes.txt", ".hidden/x.mp3")
	entries, sidecars, err := WalkMusic(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.Type != "Audio" || len(e.Files) != 1 || e.Files[0].Path != e.Path {
			t.Errorf("entry %+v", e)
		}
		got = append(got, strings.TrimPrefix(e.Path, root+"/")+"="+e.Name.Title)
	}
	want := "Albums/ABBA/Gold/01 Dancing Queen.mp3=01 Dancing Queen, Albums/ABBA/Gold/02 SOS.mp3=02 SOS, " +
		"Albums/Toto/IV/Disc 01/01 Rosanna.flac=01 Rosanna"
	if strings.Join(got, ", ") != want {
		t.Errorf("tracks:\n got %s\nwant %s", strings.Join(got, ", "), want)
	}
	var names []string
	for _, sc := range sidecars {
		names = append(names, strings.TrimPrefix(sc.Path, root+"/"))
	}
	if strings.Join(names, ", ") != "Albums/ABBA/Gold/album.nfo, Albums/ABBA/Gold/folder.jpg, Albums/ABBA/artist.nfo, "+
		"Albums/ABBA/backdrop.jpg, Albums/Toto/IV/Disc 01/01 Rosanna.lrc" {
		t.Errorf("sidecars: %s", strings.Join(names, ", "))
	}
}

func TestWalkAudiobooks(t *testing.T) {
	root := t.TempDir()
	touch(t, root,
		"Die drei Fragezeichen - Folge 1.m4b",
		"Marc Elsberg/Blackout/Blackout.m4b", "Marc Elsberg/Blackout/metadata.json", "Marc Elsberg/Blackout/cover.jpg",
		"Thomas Finn/Nebelkriege 2/10 Die Zaubernuss.mp3", "Thomas Finn/Nebelkriege 2/2 Kapitel.mp3",
		"Thomas Finn/Nebelkriege 2/1 Prolog.mp3",
		"Terry Pratchett/Scheibenwelt/01 Die Farben der Magie.m4b", "Terry Pratchett/Scheibenwelt/02 Das Licht der Phantasie.m4b")
	entries, _, err := WalkAudiobooks(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		var parts []string
		for _, f := range e.Files {
			parts = append(parts, filepath.Base(f.Path))
		}
		got = append(got, e.Name.Title+"["+strings.Join(parts, "|")+"]")
	}
	want := "Die drei Fragezeichen - Folge 1[Die drei Fragezeichen - Folge 1.m4b], Blackout[Blackout.m4b], " +
		"01 Die Farben der Magie[01 Die Farben der Magie.m4b], 02 Das Licht der Phantasie[02 Das Licht der Phantasie.m4b], " +
		"Nebelkriege 2[1 Prolog.mp3|2 Kapitel.mp3|10 Die Zaubernuss.mp3]"
	if strings.Join(got, ", ") != want {
		t.Errorf("books:\n got %s\nwant %s", strings.Join(got, ", "), want)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name.Title, "Nebelkriege") && (e.Path != filepath.Join(root, "Thomas Finn/Nebelkriege 2") ||
			e.Files[2].Part != 3) {
			t.Errorf("folder book: %+v", e)
		}
	}
}

func TestReadSidecarNFO(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "album.nfo")
	os.WriteFile(p, []byte("\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"utf-8\" standalone=\"yes\"?>\n<album>\n"+
		"  <title>Out of Africa</title>\n  <year>1997</year>\n  <genre>Soundtracks</genre>\n  <genre>Score</genre>\n"+
		"  <review />\n  <art><poster>/x/folder.jpg</poster></art>\n</album>\n"), 0o644)
	data, err := ReadSidecar(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"_root":["album"],"art":["/x/folder.jpg"],"genre":["Soundtracks","Score"],"title":["Out of Africa"],"year":["1997"]}`
	if data != want {
		t.Errorf("nfo:\n got %s\nwant %s", data, want)
	}
}

func TestNaturalLess(t *testing.T) {
	if !NaturalLess("2 a.mp3", "10 a.mp3") || NaturalLess("10 a.mp3", "2 a.mp3") || !NaturalLess("a", "B") {
		t.Error("natural order")
	}
}
