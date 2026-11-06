package store

import (
	"context"
	"testing"
	"time"
)

func TestEncodeTimes(t *testing.T) {
	for _, times := range [][]time.Duration{
		nil,
		{0},
		{-500 * time.Millisecond, 0, 2 * time.Second, 2*time.Second + time.Microsecond, 3 * time.Hour},
		{4_597_497_845 * time.Nanosecond, 8874 * time.Second},
	} {
		got, err := decodeTimes(encodeTimes(times))
		if err != nil || len(got) != len(times) {
			t.Fatalf("%v: got %v, %v", times, got, err)
		}
		for i := range times {
			if want := times[i].Truncate(time.Microsecond); got[i] != want {
				t.Errorf("%v: time %d is %v, want %v", times, i, got[i], want)
			}
		}
	}
	// Two hours with a keyframe every 2 s take three bytes each.
	var long []time.Duration
	for t := time.Duration(0); t < 2*time.Hour; t += 2 * time.Second {
		long = append(long, t)
	}
	if n := len(encodeTimes(long)); n > 3*len(long)+8 {
		t.Errorf("%d keyframes take %d bytes", len(long), n)
	}
	if _, err := decodeTimes([]byte{0x80}); err == nil {
		t.Error("truncated varint decoded")
	}
}

func TestKeyframeIndex(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	lib, _ := s.EnsureLibrary(ctx, "Filme", "movies")
	now := time.Now().UTC().Truncate(time.Millisecond)
	mtime := now.Add(-time.Hour)
	if err := s.ApplyScan(ctx, ScanChanges{NewItems: []Item{
		{ID: "m1", LibraryID: lib.ID, Type: "Movie", Path: "/m/A", Title: "A", SortTitle: "a", DateCreated: now,
			Files: []MediaFile{{ID: "f1", ItemID: "m1", Path: "/m/A/a.mkv", Size: 100, ModTime: mtime},
				{ID: "f2", ItemID: "m1", Path: "/m/A/b.mkv", Size: 200, ModTime: mtime},
				{ID: "f3", ItemID: "m1", Path: "/m/A/c.mkv", Size: 300, ModTime: mtime}}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Only probed files are indexed.
	for _, id := range []string{"f1", "f2"} {
		if err := s.SetProbeResult(ctx, MediaFile{ID: id, Size: map[string]int64{"f1": 100, "f2": 200}[id], ModTime: mtime},
			ProbeResult{JSON: "{}", Duration: time.Minute}, now); err != nil {
			t.Fatal(err)
		}
	}
	unindexed := func() []string {
		t.Helper()
		files, err := s.UnindexedFiles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range files {
			ids = append(ids, f.ID)
			if f.Duration != time.Minute {
				t.Errorf("%s: duration %v", f.ID, f.Duration)
			}
		}
		return ids
	}
	if got := unindexed(); len(got) != 2 {
		t.Fatalf("unindexed %v", got)
	}

	k := KeyframeIndex{FileID: "f1", Size: 100, ModTime: mtime, Source: "matroska",
		Times: []time.Duration{0, 2 * time.Second, 7 * time.Second}, Took: 80 * time.Millisecond, MadeAt: now}
	if err := s.SetKeyframeIndex(ctx, k); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeyframeIndex(ctx, KeyframeIndex{FileID: "f2", Size: 200, ModTime: mtime, Err: "no keyframes",
		MadeAt: now}); err != nil {
		t.Fatal(err)
	}
	// A file that is gone gets nothing stored.
	if err := s.SetKeyframeIndex(ctx, KeyframeIndex{FileID: "gone", Size: 1, ModTime: mtime, MadeAt: now}); err != nil {
		t.Fatal(err)
	}
	if got := unindexed(); len(got) != 0 {
		t.Errorf("unindexed after storing %v", got)
	}
	got, ok, err := s.KeyframeIndex(ctx, "f1", 100, mtime)
	if err != nil || !ok || got.Source != "matroska" || len(got.Times) != 3 || got.Times[2] != 7*time.Second ||
		got.Took != k.Took || !got.ModTime.Equal(mtime) || got.MaxGap() != 5*time.Second {
		t.Errorf("stored %+v, %v, %v", got, ok, err)
	}
	if _, ok, _ := s.KeyframeIndex(ctx, "f1", 101, mtime); ok {
		t.Error("index for another size")
	}
	st, err := s.KeyframeStats(ctx)
	if err != nil || st.Files != 2 || st.Indexed() != 1 || st.BySource["matroska"] != 1 || st.Failed != 1 || st.Pending != 0 {
		t.Errorf("stats %+v, %v", st, err)
	}

	// A changed file makes its index stale.
	if err := s.ApplyScan(ctx, ScanChanges{UpdatedFiles: []FileUpdate{{File: MediaFile{ID: "f1", ItemID: "m1",
		Path: "/m/A/a.mkv", Size: 150, ModTime: mtime}, ResetProbe: true}}}); err != nil {
		t.Fatal(err)
	}
	s.SetProbeResult(ctx, MediaFile{ID: "f1", Size: 150, ModTime: mtime}, ProbeResult{JSON: "{}", Duration: time.Minute}, now)
	if got := unindexed(); len(got) != 1 || got[0] != "f1" {
		t.Errorf("unindexed after a change %v", got)
	}
	if st, _ := s.KeyframeStats(ctx); st.Pending != 1 || st.Indexed() != 0 {
		t.Errorf("stats after a change %+v", st)
	}
	// Deleted files take their index with them; a reset drops them all.
	if err := s.ApplyScan(ctx, ScanChanges{DeletedFileIDs: []string{"f2"}}); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM keyframes").Scan(&n)
	if n != 1 {
		t.Errorf("%d rows after deleting a file", n)
	}
	if err := s.ResetKeyframes(ctx); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT count(*) FROM keyframes").Scan(&n)
	if n != 0 {
		t.Errorf("%d rows after reset", n)
	}
}
