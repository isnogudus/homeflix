package catalog

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"homeflix/internal/store"
)

func probe(height int, subs bool, audioLang string) string {
	s := fmt.Sprintf(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":%d,"height":%d},`+
		`{"index":1,"codec_type":"audio","codec_name":"ac3","tags":{"language":%q}}`, height*16/9, height, audioLang)
	if subs {
		s += `,{"index":2,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"ger"}}`
	}
	return s + `],"format":{"format_name":"matroska,webm","duration":"3600"}}`
}

func movie(id, title, sortTitle string, year int, created time.Time, files ...store.MediaFile) store.Item {
	for i := range files {
		files[i].ItemID = id
	}
	return store.Item{ID: id, LibraryID: "lib", Type: "Movie", Title: title, SortTitle: sortTitle, Year: year,
		DateCreated: created, Files: files}
}

func file(id, version string, part, height int, subs bool) store.MediaFile {
	return store.MediaFile{ID: id, Version: version, Part: part, Size: 1000, Probe: probe(height, subs, "ger")}
}

func testSnapshot() *Snapshot {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	items := []store.Item{
		movie("m1", "The Abyss", "abyss", 1989, day(3), file("f1", "", 0, 1080, true)),
		movie("m2", "Verdammnis", "verdammnis", 2009, day(1),
			file("f2", "SD", 1, 576, false), file("f3", "SD", 2, 576, false), file("f4", "HD", 0, 1080, false)),
		movie("m3", "Ärger", "arger", 2001, day(2), file("f5", "", 0, 2160, false)),
		movie("m4", "2 Guns", "00000002 guns", 2013, day(4), file("f6", "", 0, 720, false)),
		movie("m5", "Broken", "broken", 0, day(5)),
		{ID: "x1", LibraryID: "lib", ParentID: "m1", Type: "Video", ExtraType: "BehindTheScenes", Title: "Making of",
			SortTitle: "making of", Files: []store.MediaFile{{ID: "f7", ItemID: "x1", Probe: probe(720, false, "eng")}}},
	}
	return Build([]store.Library{{ID: "lib", Name: "Filme", CollectionType: "movies"}}, items, "en", nil)
}

func titles(ms []*Movie) string {
	var t []string
	for _, m := range ms {
		t = append(t, m.Title)
	}
	return strings.Join(t, ", ")
}

func TestBuild(t *testing.T) {
	s := testSnapshot()
	if s.Libraries[0].Movies != 4 {
		t.Errorf("library count %d, want 4 (movie without files is left out)", s.Libraries[0].Movies)
	}
	v, _ := s.Item("m2")
	if len(v.Versions) != 2 || v.Versions[0].Label != "HD" || v.Versions[0].ID != "m2" || v.Versions[1].ID != "f2" {
		t.Fatalf("versions: %+v", v.Versions)
	}
	sd := v.Versions[1]
	if len(sd.Parts) != 2 || sd.Parts[0].File.Part != 1 || sd.Size != 2000 || sd.Duration != 2*time.Hour {
		t.Errorf("SD version: %d parts, size %d, duration %s", len(sd.Parts), sd.Size, sd.Duration)
	}
	if sd.Parts[0].File.Probe != "" {
		t.Error("probe JSON kept in memory")
	}
	if m, src, ok := s.Source("f2"); !ok || m.ID != "m2" || src != sd {
		t.Error("media source lookup by file ID failed")
	}
	abyss, _ := s.Item("m1")
	if len(abyss.Extras) != 1 || abyss.Extras[0].Parent != abyss || !abyss.Extras[0].IsExtra() {
		t.Errorf("extras: %+v", abyss.Extras)
	}
	if _, ok := s.Item("x1"); !ok {
		t.Error("extra not addressable by ID")
	}
}

func TestQueries(t *testing.T) {
	s := testSnapshot()
	yes, no := true, false
	for name, tc := range map[string]struct {
		q     Query
		want  string
		total int
	}{
		"by name":         {Query{SortBy: []string{"SortName"}}, "2 Guns, The Abyss, Ärger, Verdammnis", 4},
		"by year desc":    {Query{SortBy: []string{"ProductionYear", "SortName"}, Descending: []bool{true, false}}, "2 Guns, Verdammnis, Ärger, The Abyss", 4},
		"added desc":      {Query{SortBy: []string{"DateCreated"}, Descending: []bool{true}}, "2 Guns, The Abyss, Ärger, Verdammnis", 4},
		"paged":           {Query{SortBy: []string{"SortName"}, StartIndex: 1, Limit: 2}, "The Abyss, Ärger", 4},
		"past the end":    {Query{StartIndex: 10}, "", 4},
		"search accent":   {Query{Search: "arger"}, "Ärger", 1},
		"search partial":  {Query{Search: "ABY"}, "The Abyss", 1},
		"starts with a":   {Query{NameStartsWith: "A"}, "The Abyss, Ärger", 2},
		"numbers":         {Query{NameLessThan: "A"}, "2 Guns", 1},
		"years":           {Query{Years: []int{2001, 2009}}, "Ärger, Verdammnis", 2},
		"ids":             {Query{IDs: []string{"m3", "m1"}}, "The Abyss, Ärger", 2},
		"subtitles":       {Query{HasSubtitles: &yes}, "The Abyss", 1},
		"no subtitles":    {Query{HasSubtitles: &no}, "2 Guns, Ärger, Verdammnis", 3},
		"4k":              {Query{Is4K: &yes}, "Ärger", 1},
		"not hd":          {Query{IsHD: &no}, "", 0},
		"other library":   {Query{LibraryID: "other"}, "", 0},
		"audio language":  {Query{AudioLanguages: []string{"GER"}}, "2 Guns, The Abyss, Ärger, Verdammnis", 4},
		"unknown sort ok": {Query{SortBy: []string{"CommunityRating"}}, "2 Guns, The Abyss, Ärger, Verdammnis", 4},
	} {
		t.Run(name, func(t *testing.T) {
			got, total := s.Movies(tc.q)
			if titles(got) != tc.want || total != tc.total {
				t.Errorf("got %q (%d), want %q (%d)", titles(got), total, tc.want, tc.total)
			}
		})
	}
	if got := titles(s.Latest("lib", 2)); got != "2 Guns, The Abyss" {
		t.Errorf("latest: %q", got)
	}
	if fmt.Sprint(s.Years("")) != "[1989 2001 2009 2013]" {
		t.Errorf("years: %v", s.Years(""))
	}
}

func TestSearchKey(t *testing.T) {
	for in, want := range map[string]string{
		"Wall·E":                   "wall e",
		"Die Säulen der Erde":      "die saulen der erde",
		"  Straße  ":               "strasse",
		"CAT. 8 - Wenn die Erde …": "cat 8 wenn die erde",
	} {
		if got := SearchKey(in); got != want {
			t.Errorf("SearchKey(%q) = %q, want %q", in, got, want)
		}
	}
}
