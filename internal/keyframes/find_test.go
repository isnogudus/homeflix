package keyframes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// fakeFFprobe writes a script that prints packets as ffprobe does, with
// keyframes at 0 and 5 s, and counts its runs in the file calls.
func fakeFFprobe(t *testing.T) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	bin, calls = filepath.Join(dir, "ffprobe"), filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho run >> '" + calls + "'\n" +
		"echo 'pts_time=N/A|dts_time=0.000000|flags=K__'\n" +
		"echo 'pts_time=N/A|dts_time=0.040000|flags=___'\n" +
		"echo 'pts_time=5.000000|dts_time=4.960000|flags=K_C'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func runs(calls string) int {
	b, _ := os.ReadFile(calls)
	return strings.Count(string(b), "run")
}

func TestTicks(t *testing.T) {
	// The timescale of a real MP4: a multiplication first overflows.
	const ts = 2005840778
	if got := ticks(8874*ts+ts/2, ts); got != 8874*time.Second+500*time.Millisecond {
		t.Errorf("ticks %v", got)
	}
	if got := ticks(3, 90000); got != 33333*time.Nanosecond {
		t.Errorf("ticks %v", got)
	}
}

func TestCheck(t *testing.T) {
	every := func(step, end time.Duration) []time.Duration {
		var out []time.Duration
		for t := time.Duration(0); t <= end; t += step {
			out = append(out, t)
		}
		return out
	}
	for _, c := range []struct {
		name     string
		times    []time.Duration
		duration time.Duration
		bad      string
	}{
		{"fine", every(10*time.Second, 7190*time.Second), 7200 * time.Second, ""},
		{"no duration", every(10*time.Second, 600*time.Second), 0, ""},
		{"empty", nil, 0, "no keyframes"},
		{"overflowed", []time.Duration{sec(-4.597), 0, sec(4.598)}, 8874 * time.Second, "before the start"},
		{"late start", []time.Duration{sec(90), sec(100)}, 0, "first keyframe"},
		{"hole", []time.Duration{0, sec(10), sec(200), sec(210)}, 0, "no keyframe for 3m10s"},
		{"cut short", every(10*time.Second, 5550*time.Second), 5979 * time.Second, "before the end"},
		{"past the end", []time.Duration{0, sec(30), sec(90)}, sec(60), "after the end"},
		{"every frame", every(41708*time.Microsecond, 600*time.Second), 600 * time.Second, "every frame is flagged"},
		{"DVD GOPs", every(480*time.Millisecond, 600*time.Second), 600 * time.Second, ""},
		{"short intra clip", every(40*time.Millisecond, 30*time.Second), 30 * time.Second, ""},
	} {
		err := Check(c.times, c.duration)
		switch {
		case c.bad == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("%s: %v, want %q", c.name, err, c.bad)
		}
	}
}

func TestFind(t *testing.T) {
	ffprobe, calls := fakeFFprobe(t)
	f := Finder{FFprobe: ffprobe}
	ctx := context.Background()

	// A good index is taken as it is.
	idx, err := f.Find(ctx, filepath.Join("testdata", "ffmpeg.mkv"), sec(10))
	if err != nil || idx.Source != SourceMatroska || len(idx.Times) != 5 || idx.Note != "" {
		t.Errorf("matroska: %+v, %v", idx, err)
	}
	idx, err = f.Find(ctx, filepath.Join("testdata", "faststart.mp4"), 0)
	if err != nil || idx.Source != SourceMP4 || len(idx.Times) != 5 {
		t.Errorf("mp4: %+v, %v", idx, err)
	}
	if n := runs(calls); n != 0 {
		t.Errorf("ffprobe ran %d times for files with an index", n)
	}

	// An index that covers a tenth of the file is not believed; the scan
	// is taken although it does not cover the file either.
	idx, err = f.Find(ctx, filepath.Join("testdata", "moovlast.mp4"), sec(100))
	if err != nil || idx.Source != SourceFFprobe || len(idx.Times) != 2 || idx.Times[1] != 5*time.Second ||
		!strings.Contains(idx.Note, "mp4 index rejected: last keyframe at 8s") || !strings.Contains(idx.Note, "; packets: last keyframe at 5s") {
		t.Errorf("short index: %+v, %v", idx, err)
	}

	// Cues that list every frame, as mkvmerge --cues 0:all writes them,
	// are no keyframe index.
	idx, err = f.Find(ctx, filepath.Join("testdata", "allcues.mkv"), sec(10))
	if err != nil || idx.Source != SourceFFprobe || !strings.Contains(idx.Note, "is not a keyframe") {
		t.Errorf("all cues: %+v, %v", idx, err)
	}

	// A container without index is scanned.
	avi := filepath.Join(t.TempDir(), "movie.avi")
	os.WriteFile(avi, []byte("RIFF....AVI LIST"), 0o644)
	idx, err = f.Find(ctx, avi, sec(10))
	if err != nil || idx.Source != SourceFFprobe || len(idx.Times) != 2 || idx.Times[0] != 0 ||
		!strings.Contains(idx.Note, "unknown container") {
		t.Errorf("avi: %+v, %v", idx, err)
	}
	if _, err := (Finder{}).Find(ctx, avi, 0); err == nil || !strings.Contains(err.Error(), "unknown container") {
		t.Errorf("avi without ffprobe: %v", err)
	}

	// Scans wait for a free slot.
	busy := Finder{FFprobe: ffprobe, Scans: make(chan struct{}, 1)}
	busy.Scans <- struct{}{}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := busy.Find(short, avi, 0); err != context.DeadlineExceeded {
		t.Errorf("scan without a free slot: %v", err)
	}
	<-busy.Scans
	if idx, err := busy.Find(ctx, avi, 0); err != nil || len(idx.Times) != 2 || len(busy.Scans) != 0 {
		t.Errorf("scan with a free slot: %+v, %v, %d taken", idx, err, len(busy.Scans))
	}

	// A missing file is not scanned.
	before := runs(calls)
	if _, err := f.Find(ctx, filepath.Join(t.TempDir(), "gone.mkv"), 0); err == nil {
		t.Error("missing file")
	}
	if runs(calls) != before {
		t.Error("ffprobe ran for a missing file")
	}
}

func TestScanFails(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffprobe")
	os.WriteFile(bin, []byte("#!/bin/sh\necho 'Invalid data found when processing input' >&2\nexit 1\n"), 0o755)
	if _, err := Scan(context.Background(), bin, "/m/x.avi"); err == nil || !strings.Contains(err.Error(), "Invalid data") {
		t.Errorf("failing ffprobe: %v", err)
	}
	os.WriteFile(bin, []byte("#!/bin/sh\necho 'pts_time=1.0|dts_time=1.0|flags=___'\n"), 0o755)
	if _, err := Scan(context.Background(), bin, "/m/x.avi"); err == nil || !strings.Contains(err.Error(), "no video keyframes") {
		t.Errorf("no keyframes: %v", err)
	}
}

func TestKeyPacket(t *testing.T) {
	for line, want := range map[string]time.Duration{
		"pts_time=12.345678|dts_time=12.300000|flags=K__": sec(12.345678),
		"pts_time=N/A|dts_time=3.000000|flags=K__":        3 * time.Second,
		"pts_time=-0.083000|dts_time=-0.125000|flags=KD_": sec(-0.083),
	} {
		if got, ok := keyPacket(line); !ok || got != want {
			t.Errorf("%s: %v, %v", line, got, ok)
		}
	}
	for _, line := range []string{"pts_time=1.0|dts_time=1.0|flags=___", "pts_time=N/A|dts_time=N/A|flags=K__", ""} {
		if _, ok := keyPacket(line); ok {
			t.Errorf("%q taken as a keyframe", line)
		}
	}
}
