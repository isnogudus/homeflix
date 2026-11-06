package keyframes

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"homeflix/internal/store"
)

func TestIndexer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ffprobe, calls := fakeFFprobe(t)

	mkv := filepath.Join(dir, "a.mkv")
	data, _ := os.ReadFile(filepath.Join("testdata", "ffmpeg.mkv"))
	os.WriteFile(mkv, data, 0o644)
	avi := filepath.Join(dir, "b.avi")
	os.WriteFile(avi, []byte("RIFF....AVI LIST"), 0o644)
	files := map[string]string{"f1": mkv, "f2": avi}
	lib, _ := db.EnsureLibrary(ctx, "Filme", "movies")
	now := time.Now().UTC().Truncate(time.Millisecond)
	it := store.Item{ID: "m1", LibraryID: lib.ID, Type: "Movie", Path: dir, Title: "A", SortTitle: "a", DateCreated: now}
	stat := map[string]store.MediaFile{}
	for id, path := range files {
		fi, _ := os.Stat(path)
		f := store.MediaFile{ID: id, ItemID: "m1", Path: path, Size: fi.Size(), ModTime: fi.ModTime(),
			Duration: 10 * time.Second}
		it.Files = append(it.Files, f)
		stat[id] = f
	}
	if err := db.ApplyScan(ctx, store.ScanChanges{NewItems: []store.Item{it}}); err != nil {
		t.Fatal(err)
	}
	for _, f := range it.Files {
		db.SetProbeResult(ctx, f, store.ProbeResult{JSON: "{}", Duration: 10 * time.Second}, now)
	}

	ix := &Indexer{Store: db, Finder: Finder{FFprobe: ffprobe, Scans: make(chan struct{}, 1)}, Workers: 2}
	// Players asking at once share one scan.
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if times, err := ix.Get(ctx, stat["f2"]); err != nil || len(times) != 2 {
				t.Errorf("get: %v, %v", times, err)
			}
		}()
	}
	wg.Wait()
	if n := runs(calls); n != 1 {
		t.Errorf("ffprobe ran %d times", n)
	}
	k, ok, _ := db.KeyframeIndex(ctx, "f2", stat["f2"].Size, stat["f2"].ModTime)
	if !ok || k.Source != SourceFFprobe || k.Note == "" {
		t.Errorf("stored %+v, %v", k, ok)
	}

	rep, err := ix.IndexAll(ctx)
	if err != nil || rep.Indexed != 1 || rep.Scanned != 0 || rep.Failed != 0 {
		t.Errorf("index all: %+v, %v", rep, err)
	}
	if rep, _ := ix.IndexAll(ctx); rep.Indexed+rep.Scanned+rep.Failed != 0 {
		t.Errorf("second run did work: %+v", rep)
	}
	// Stored keyframes are used from then on.
	if times, err := ix.Get(ctx, stat["f2"]); err != nil || len(times) != 2 || runs(calls) != 1 {
		t.Errorf("stored get: %v, %v, %d runs", times, err, runs(calls))
	}
	st, _ := db.KeyframeStats(ctx)
	if st.Indexed() != 2 || st.BySource[SourceMatroska] != 1 || st.BySource[SourceFFprobe] != 1 || st.Noted != 1 {
		t.Errorf("stats %+v", st)
	}

	// A file whose keyframes cannot be found is recorded as such and not
	// tried again until it changes.
	os.WriteFile(ffprobe, []byte("#!/bin/sh\necho run >> '"+calls+"'\necho 'moov atom not found' >&2\nexit 1\n"), 0o755)
	os.WriteFile(avi, []byte("RIFF....AVI LIST, longer"), 0o644)
	fi, _ := os.Stat(avi)
	changed := stat["f2"]
	changed.Size, changed.ModTime = fi.Size(), fi.ModTime()
	if err := db.ApplyScan(ctx, store.ScanChanges{UpdatedFiles: []store.FileUpdate{{File: changed, ResetProbe: true}}}); err != nil {
		t.Fatal(err)
	}
	db.SetProbeResult(ctx, changed, store.ProbeResult{JSON: "{}", Duration: 10 * time.Second}, now)
	rep, err = ix.IndexAll(ctx)
	if err != nil || rep.Failed != 1 {
		t.Errorf("index all after a change: %+v, %v", rep, err)
	}
	before := runs(calls)
	if _, err := ix.Get(ctx, changed); err == nil {
		t.Error("no error for a file without keyframes")
	}
	if runs(calls) != before {
		t.Error("a failed file was scanned again")
	}
	if st, _ := db.KeyframeStats(ctx); st.Failed != 1 || st.Indexed() != 1 {
		t.Errorf("stats after failure %+v", st)
	}
}

func TestIndexAllScansLast(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ffprobe, calls := fakeFFprobe(t)
	data, _ := os.ReadFile(filepath.Join("testdata", "ffmpeg.mkv"))
	lib, _ := db.EnsureLibrary(ctx, "Filme", "movies")
	now := time.Now().UTC().Truncate(time.Millisecond)
	it := store.Item{ID: "m1", LibraryID: lib.ID, Type: "Movie", Path: dir, Title: "A", SortTitle: "a", DateCreated: now}
	// Files sorted by path: the one to scan comes first.
	for i, name := range []string{"a.avi", "b.mkv", "c.mkv"} {
		path := filepath.Join(dir, name)
		if name == "a.avi" {
			os.WriteFile(path, []byte("RIFF....AVI LIST"), 0o644)
		} else {
			os.WriteFile(path, data, 0o644)
		}
		fi, _ := os.Stat(path)
		it.Files = append(it.Files, store.MediaFile{ID: "f" + string(rune('1'+i)), ItemID: "m1", Path: path,
			Size: fi.Size(), ModTime: fi.ModTime()})
	}
	if err := db.ApplyScan(ctx, store.ScanChanges{NewItems: []store.Item{it}}); err != nil {
		t.Fatal(err)
	}
	for _, f := range it.Files {
		db.SetProbeResult(ctx, f, store.ProbeResult{JSON: "{}", Duration: 10 * time.Second}, now)
	}
	ix := &Indexer{Store: db, Finder: Finder{FFprobe: ffprobe, Scans: make(chan struct{}, 1)}, Workers: 1}
	rep, err := ix.IndexAll(ctx)
	if err != nil || rep.Indexed != 2 || rep.Scanned != 1 || rep.Failed != 0 || runs(calls) != 1 {
		t.Errorf("index all: %+v, %v, %d scans", rep, err, runs(calls))
	}
	k, ok, _ := db.KeyframeIndex(ctx, "f1", it.Files[0].Size, it.Files[0].ModTime)
	k2, _, _ := db.KeyframeIndex(ctx, "f3", it.Files[2].Size, it.Files[2].ModTime)
	if !ok || k.Source != SourceFFprobe || !k.MadeAt.After(k2.MadeAt.Add(-time.Millisecond)) {
		t.Errorf("scanned %+v after %+v", k, k2)
	}
}
