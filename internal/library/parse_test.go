package library

import (
	"sort"
	"testing"
)

func TestParseName(t *testing.T) {
	tests := []struct {
		name  string
		isDir bool
		want  Name
	}{
		// Names as they occur in the library this server was built for.
		{"12 Monkeys (1995).mp4", false, Name{Title: "12 Monkeys", Year: 1995}},
		{"16 Uhr 50 ab Paddington (1961) [imdbid-tt0055205].mkv", false, Name{Title: "16 Uhr 50 ab Paddington", Year: 1961, IMDbID: "tt0055205"}},
		{"#9 (2009).avi [imdbid-tt0472033].mp4", false, Name{Title: "#9", Year: 2009, IMDbID: "tt0472033", Version: "avi"}},
		{"Alien³ (1992) [imdbid-tt0103644].mkv", false, Name{Title: "Alien³", Year: 1992, IMDbID: "tt0103644"}},
		{"2010 - Das Jahr, in dem wir Kontakt aufnehmen (1984).mp4", false, Name{Title: "2010 - Das Jahr, in dem wir Kontakt aufnehmen", Year: 1984}},
		{"1984 (1984).mp4", false, Name{Title: "1984", Year: 1984}},
		{"Anaconda (2025) [imdbid-tt33244668].mkv", false, Name{Title: "Anaconda", Year: 2025, IMDbID: "tt33244668"}},
		{"Animal Farm - Aufstand der Tiere.mp4", false, Name{Title: "Animal Farm - Aufstand der Tiere"}},
		{"Verblendung (2009) [SD] part1.mp4", false, Name{Title: "Verblendung", Year: 2009, Part: 1, Version: "SD"}},
		{"Verblendung (2009) [HD].mp4", false, Name{Title: "Verblendung", Year: 2009, Version: "HD"}},
		{"Der weiße Hai 2 (1978) [new voices] [HD].mp4", false, Name{Title: "Der weiße Hai 2", Year: 1978, Version: "new voices, HD"}},
		{"The Time Machine (2002) MPEG-480p MPEG Audio-2ch part2.mpeg", false, Name{Title: "The Time Machine", Year: 2002, Part: 2, Version: "MPEG-480p MPEG Audio-2ch"}},
		{"CAT. 8 - Wenn die Erde verglüht … (2013) part1.mp4", false, Name{Title: "CAT. 8 - Wenn die Erde verglüht …", Year: 2013, Part: 1}},
		{"Die zehn Gebote - part 2.mkv", false, Name{Title: "Die zehn Gebote", Part: 2}},
		{"Ben Hur - part 1.mkv", false, Name{Title: "Ben Hur", Part: 1}},
		{"Ben Hur- 50th Anniversary UCE", true, Name{Title: "Ben Hur- 50th Anniversary UCE"}},
		{"Die zehn Gebote (1956) [imdbid-tt0049833]", true, Name{Title: "Die zehn Gebote", Year: 1956, IMDbID: "tt0049833"}},
		{"Sade - Bring Me Home - Live 2011 (2012) [imdbid-tt3509240]", true, Name{Title: "Sade - Bring Me Home - Live 2011", Year: 2012, IMDbID: "tt3509240"}},
		// Other common conventions.
		{"Heat (1995) {imdb-tt0113277}.mkv", false, Name{Title: "Heat", Year: 1995, IMDbID: "tt0113277"}},
		{"Heat (1995) [tmdbid-949].mkv", false, Name{Title: "Heat", Year: 1995, TMDbID: "949"}},
		{"Heat (1995) [tt0113277] cd2.avi", false, Name{Title: "Heat", Year: 1995, IMDbID: "tt0113277", Part: 2}},
		{"Space Odyssey (3001).mkv", false, Name{Title: "Space Odyssey (3001)"}},
		{"Smartphone.mkv", false, Name{Title: "Smartphone"}},
		{"(2019).mkv", false, Name{Title: "(2019)", Year: 2019}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseName(tc.name, tc.isDir); got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestSortKey(t *testing.T) {
	articles := []string{"the", "a", "an"}
	for title, want := range map[string]string{
		"The Time Machine":                  "time machine",
		"A":                                 "a",
		"Ärger im Paradies":                 "arger im paradies",
		"Der weiße Hai 2":                   "der weisse hai 00000002",
		"#9":                                "00000009",
		"Alien³":                            "alien00000003",
		"CAT. 8 - Wenn die Erde verglüht …": "cat 00000008 wenn die erde vergluht",
	} {
		if got := SortKey(title, articles); got != want {
			t.Errorf("SortKey(%q) = %q, want %q", title, got, want)
		}
	}

	titles := []string{"10 Minutes Gone", "2 Guns", "The Abyss", "Ärger", "Zorro", "Alien", "#9", "2012"}
	sort.Slice(titles, func(i, j int) bool { return SortKey(titles[i], articles) < SortKey(titles[j], articles) })
	want := []string{"2 Guns", "#9", "10 Minutes Gone", "2012", "The Abyss", "Alien", "Ärger", "Zorro"}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("order %q, want %q", titles, want)
		}
	}
}
