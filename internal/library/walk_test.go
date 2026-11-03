package library

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// makeTree creates empty files (and their directories) below root.
func makeTree(t *testing.T, root string, paths ...string) {
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

func describe(root string, e Entry) string {
	rel := func(p string) string { r, _ := filepath.Rel(root, p); return r }
	var b strings.Builder
	b.WriteString(rel(e.Path) + " => " + e.Name.Title)
	if e.Name.Year != 0 {
		b.WriteString(" (" + itoa(e.Name.Year) + ")")
	}
	if e.Name.IMDbID != "" {
		b.WriteString(" " + e.Name.IMDbID)
	}
	for _, f := range e.Files {
		b.WriteString(" | " + f.Version + "#" + itoa(f.Part) + " " + filepath.Base(f.Path))
	}
	for _, x := range e.Extras {
		b.WriteString(" | extra " + x.Type + " " + x.Title)
	}
	return b.String()
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestWalk(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root,
		"12 Monkeys (1995).mp4",
		"#9 (2009).avi [imdbid-tt0472033].mp4",
		"notes.txt",
		".hidden.mp4",
		"@eaDir/thumb.mp4",
		"Loose (2001) part1.mkv",
		"Loose (2001) part2.mkv",
		"Delete (2013) [imdbid-tt2316306]/Delete (2013) part1.mp4",
		"Delete (2013) [imdbid-tt2316306]/Delete (2013) part2.mp4",
		"Verdammnis (2009) [imdbid-tt1216487]/Verdammnis (2009) [HD].mp4",
		"Verdammnis (2009) [imdbid-tt1216487]/Verdammnis (2009) [SD] part1.mp4",
		"Verdammnis (2009) [imdbid-tt1216487]/Verdammnis (2009) [SD] part2.mp4",
		"Ben Hur- 50th Anniversary UCE/Ben Hur - part 1.mkv",
		"Ben Hur- 50th Anniversary UCE/Ben Hur - part 2.mkv",
		"Sade (2012) [imdbid-tt3509240]/Sade (2012) [imdbid-tt3509240].mkv",
		"Sade (2012) [imdbid-tt3509240]/behind the scenes/In the Trenches.mkv",
		"Sade (2012) [imdbid-tt3509240]/random/ignored.mkv",
		"Twins/Twins.mkv",
		"Twins/Twins.mp4",
		"Collection/Alien (1979)/Alien (1979).mkv",
		"Collection/Aliens (1986).mkv/Aliens (1986).mkv",
		"Folder Year (2020)/movie.mkv",
	)
	entries, err := Walk(root, discard)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, describe(root, e))
	}
	want := []string{
		"#9 (2009).avi [imdbid-tt0472033].mp4 => #9 (2009) tt0472033 | #0 #9 (2009).avi [imdbid-tt0472033].mp4",
		"12 Monkeys (1995).mp4 => 12 Monkeys (1995) | #0 12 Monkeys (1995).mp4",
		"Ben Hur- 50th Anniversary UCE => Ben Hur- 50th Anniversary UCE | #1 Ben Hur - part 1.mkv | #2 Ben Hur - part 2.mkv",
		"Collection/Alien (1979) => Alien (1979) | #0 Alien (1979).mkv",
		"Collection/Aliens (1986).mkv => Aliens (1986) | #0 Aliens (1986).mkv",
		"Delete (2013) [imdbid-tt2316306] => Delete (2013) tt2316306 | #1 Delete (2013) part1.mp4 | #2 Delete (2013) part2.mp4",
		"Folder Year (2020) => Folder Year (2020) | #0 movie.mkv",
		"Loose (2001) part1.mkv => Loose (2001) | #1 Loose (2001) part1.mkv | #2 Loose (2001) part2.mkv",
		"Sade (2012) [imdbid-tt3509240] => Sade (2012) tt3509240 | #0 Sade (2012) [imdbid-tt3509240].mkv | extra BehindTheScenes In the Trenches",
		"Twins => Twins | Twins.mkv#0 Twins.mkv | Twins.mp4#0 Twins.mp4",
		"Verdammnis (2009) [imdbid-tt1216487] => Verdammnis (2009) tt1216487 | HD#0 Verdammnis (2009) [HD].mp4 | SD#1 Verdammnis (2009) [SD] part1.mp4 | SD#2 Verdammnis (2009) [SD] part2.mp4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWalkMissingRoot(t *testing.T) {
	if _, err := Walk(filepath.Join(t.TempDir(), "absent"), discard); err == nil {
		t.Error("want error for missing root")
	}
}
