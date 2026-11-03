package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"homeflix/internal/config"
	"homeflix/internal/lockfile"
	"homeflix/internal/store"
)

type fakeProber struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (p *fakeProber) Probe(ctx context.Context, path string) store.ProbeResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, path)
	if p.fail[filepath.Base(path)] {
		return store.ProbeResult{Err: "broken"}
	}
	return store.ProbeResult{JSON: `{"streams":[{"index":0}]}`, Container: "matroska,webm", Duration: time.Hour}
}

func (p *fakeProber) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.calls
	p.calls = nil
	return c
}

type scanFixture struct {
	t      *testing.T
	root   string
	store  *store.Store
	prober *fakeProber
	sc     *Scanner
	now    time.Time
}

func newScanFixture(t *testing.T) *scanFixture {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "movies")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &scanFixture{t: t, root: root, store: db, prober: &fakeProber{fail: map[string]bool{}}, now: time.Now()}
	f.sc = &Scanner{
		Store:        db,
		Libraries:    []config.Library{{Name: "Filme", Paths: []string{root}}},
		Prober:       f.prober,
		Workers:      2,
		SortArticles: []string{"the"},
		LockPath:     filepath.Join(dir, "data", "scan.lock"),
		Log:          discard,
		Now:          func() time.Time { return f.now },
	}
	return f
}

func (f *scanFixture) scan() LibraryReport {
	f.t.Helper()
	rep, err := f.sc.Scan(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return rep.Libraries[0]
}

func (f *scanFixture) write(rel, content string) {
	f.t.Helper()
	makeTree(f.t, f.root, rel)
	if err := os.WriteFile(filepath.Join(f.root, rel), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *scanFixture) move(from, to string) {
	f.t.Helper()
	full := filepath.Join(f.root, to)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.root, from), full); err != nil {
		f.t.Fatal(err)
	}
}

// items returns the stored items keyed by path relative to the root.
func (f *scanFixture) items() map[string]store.Item {
	f.t.Helper()
	libs, err := f.store.Libraries(context.Background())
	if err != nil || len(libs) != 1 {
		f.t.Fatalf("libraries: %v %v", libs, err)
	}
	items, err := f.store.LibraryItems(context.Background(), libs[0].ID)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]store.Item{}
	for _, it := range items {
		rel, _ := filepath.Rel(f.root, it.Path)
		out[rel] = it
	}
	return out
}

func TestScanAddsAndProbes(t *testing.T) {
	f := newScanFixture(t)
	f.write("The Abyss (1989).mkv", "abyss")
	f.write("Delete (2013) [imdbid-tt2316306]/Delete (2013) part1.mp4", "d1")
	f.write("Delete (2013) [imdbid-tt2316306]/Delete (2013) part2.mp4", "d2")
	f.write("Sade (2012)/Sade (2012).mkv", "sade")
	f.write("Sade (2012)/behind the scenes/Interview.mkv", "bts")
	f.prober.fail["Interview.mkv"] = true

	rep := f.scan()
	if rep.Movies != 3 || rep.Added != 3 || rep.FilesAdded != 5 {
		t.Errorf("first scan: %+v", rep)
	}
	if got := len(f.prober.take()); got != 5 {
		t.Errorf("probed %d files, want 5", got)
	}
	items := f.items()
	abyss := items["The Abyss (1989).mkv"]
	if abyss.Title != "The Abyss" || abyss.SortTitle != "abyss" || abyss.Year != 1989 || abyss.Type != "Movie" {
		t.Errorf("abyss: %+v", abyss)
	}
	del := items["Delete (2013) [imdbid-tt2316306]"]
	if del.IMDbID != "tt2316306" || len(del.Files) != 2 || del.Files[0].Part != 1 || del.Files[1].Part != 2 {
		t.Errorf("delete: %+v", del)
	}
	extra := items["Sade (2012)/behind the scenes/Interview.mkv"]
	if extra.ParentID != items["Sade (2012)"].ID || extra.Type != "Video" || extra.ExtraType != "BehindTheScenes" {
		t.Errorf("extra: %+v", extra)
	}

	// Nothing changed: nothing is written or probed again, the failed
	// probe included.
	rep = f.scan()
	if rep.Added+rep.Updated+rep.Moved+rep.Missing+rep.FilesAdded+rep.FilesChanged+rep.FilesRemoved != 0 {
		t.Errorf("unchanged rescan: %+v", rep)
	}
	if calls := f.prober.take(); len(calls) != 0 {
		t.Errorf("unchanged rescan probed %v", calls)
	}
}

func TestScanKeepsIDsAcrossRenames(t *testing.T) {
	f := newScanFixture(t)
	f.write("Heat (1995).mkv", "heat")
	f.write("Alien (1979) [imdbid-tt0078748].mkv", "alien-v1")
	f.write("Loose (2001) part1.mkv", "l1")
	f.write("Loose (2001) part2.mkv", "l2")
	f.scan()
	before := f.items()
	f.prober.take()

	// Rename keeps size and mtime; moving into a folder too.
	f.move("Heat (1995).mkv", "Heat (1995) [imdbid-tt0113277].mkv")
	f.move("Loose (2001) part1.mkv", "Loose (2001)/Loose (2001) part1.mkv")
	f.move("Loose (2001) part2.mkv", "Loose (2001)/Loose (2001) part2.mkv")
	// A re-encode replaces the file with different content under a new
	// name; the IMDb ID ties it to the old item.
	os.Remove(filepath.Join(f.root, "Alien (1979) [imdbid-tt0078748].mkv"))
	f.write("Alien (1979) [imdbid-tt0078748] [remux].mkv", "alien-v2-longer")

	rep := f.scan()
	after := f.items()
	if rep.Moved != 3 || rep.Added != 0 || rep.Missing != 0 {
		t.Errorf("rescan: %+v", rep)
	}
	pairs := map[string]string{
		"Heat (1995).mkv":                     "Heat (1995) [imdbid-tt0113277].mkv",
		"Loose (2001) part1.mkv":              "Loose (2001)",
		"Alien (1979) [imdbid-tt0078748].mkv": "Alien (1979) [imdbid-tt0078748] [remux].mkv",
	}
	for old, cur := range pairs {
		if before[old].ID == "" || before[old].ID != after[cur].ID {
			t.Errorf("%s -> %s: ID %q became %q", old, cur, before[old].ID, after[cur].ID)
		}
	}
	if after["Heat (1995) [imdbid-tt0113277].mkv"].IMDbID != "tt0113277" {
		t.Error("IMDb ID from the new name not stored")
	}
	// Only the re-encoded file is probed again.
	if calls := f.prober.take(); len(calls) != 1 || filepath.Base(calls[0]) != "Alien (1979) [imdbid-tt0078748] [remux].mkv" {
		t.Errorf("probed after rename: %v", calls)
	}
}

func TestScanContentChangeReprobes(t *testing.T) {
	f := newScanFixture(t)
	f.write("Heat (1995).mkv", "v1")
	f.scan()
	id := f.items()["Heat (1995).mkv"].ID
	f.prober.take()

	f.write("Heat (1995).mkv", "version two")
	rep := f.scan()
	if rep.FilesChanged != 1 || f.items()["Heat (1995).mkv"].ID != id {
		t.Errorf("content change: %+v", rep)
	}
	if calls := f.prober.take(); len(calls) != 1 {
		t.Errorf("want one reprobe, got %v", calls)
	}
}

func TestScanMissingReturnAndPurge(t *testing.T) {
	f := newScanFixture(t)
	f.write("Heat (1995).mkv", "heat")
	f.write("Verdammnis (2009)/Verdammnis (2009) [SD] part1.mp4", "p1")
	f.write("Verdammnis (2009)/Verdammnis (2009) [SD] part2.mp4", "p2")
	f.write("Verdammnis (2009)/Verdammnis (2009) [HD].mp4", "hd")
	f.scan()
	id := f.items()["Heat (1995).mkv"].ID

	f.move("Heat (1995).mkv", "../Heat (1995).mkv")
	os.Remove(filepath.Join(f.root, "Verdammnis (2009)/Verdammnis (2009) [SD] part2.mp4"))
	rep := f.scan()
	if rep.Missing != 1 || rep.FilesRemoved != 1 {
		t.Errorf("after removal: %+v", rep)
	}
	heat := f.items()["Heat (1995).mkv"]
	if heat.MissingSince.IsZero() {
		t.Error("vanished movie not marked missing")
	}
	if n := len(f.items()["Verdammnis (2009)"].Files); n != 2 {
		t.Errorf("Verdammnis has %d files, want 2", n)
	}

	f.move("../Heat (1995).mkv", "Heat (1995).mkv")
	rep = f.scan()
	heat = f.items()["Heat (1995).mkv"]
	if rep.Returned != 1 || heat.ID != id || !heat.MissingSince.IsZero() {
		t.Errorf("after return: %+v, item %+v", rep, heat)
	}

	f.move("Heat (1995).mkv", "../Heat (1995).mkv")
	f.scan()
	f.now = f.now.Add(DefaultMissingGrace + time.Hour)
	rep2, err := f.sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.items()["Heat (1995).mkv"]; ok || rep2.Purged != 1 {
		t.Errorf("item not purged after grace period: purged %d", rep2.Purged)
	}
}

func TestScanUnavailableRootKeepsItems(t *testing.T) {
	f := newScanFixture(t)
	f.write("Heat (1995).mkv", "heat")
	f.scan()
	hidden := f.root + ".unmounted"
	if err := os.Rename(f.root, hidden); err != nil {
		t.Fatal(err)
	}
	rep := f.scan()
	if rep.Missing != 0 || len(rep.UnavailablePaths) != 1 {
		t.Errorf("unavailable root: %+v", rep)
	}
	if !f.items()["Heat (1995).mkv"].MissingSince.IsZero() {
		t.Error("item marked missing while its root was unavailable")
	}
}

func TestScanExtrasFollowFolder(t *testing.T) {
	f := newScanFixture(t)
	f.write("Sade (2012)/Sade (2012).mkv", "sade")
	f.write("Sade (2012)/behind the scenes/One.mkv", "one")
	f.write("Sade (2012)/behind the scenes/Two.mkv", "two")
	f.scan()
	os.Remove(filepath.Join(f.root, "Sade (2012)/behind the scenes/One.mkv"))
	f.scan()
	items := f.items()
	if _, ok := items["Sade (2012)/behind the scenes/One.mkv"]; ok {
		t.Error("removed extra still stored")
	}
	if items["Sade (2012)/behind the scenes/Two.mkv"].ParentID != items["Sade (2012)"].ID {
		t.Error("remaining extra lost its parent")
	}
}

func TestScanIsExclusive(t *testing.T) {
	f := newScanFixture(t)
	if !f.sc.mu.TryLock() {
		t.Fatal("lock unexpectedly held")
	}
	_, err := f.sc.Scan(context.Background())
	f.sc.mu.Unlock()
	if !errors.Is(err, ErrScanRunning) {
		t.Errorf("concurrent scan in one process: %v", err)
	}

	// A second process holds the lock file.
	unlock, err := lockfile.TryLock(f.sc.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sc.Scan(context.Background()); !errors.Is(err, ErrScanRunning) {
		t.Errorf("scan while lock file is held: %v", err)
	}
	unlock()
	if _, err := f.sc.Scan(context.Background()); err != nil {
		t.Errorf("scan after unlock: %v", err)
	}
}
