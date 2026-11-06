package hls

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"homeflix/internal/keyframes"
)

func sec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func TestPlanSegments(t *testing.T) {
	var kf []time.Duration
	for i := 0; i <= 58; i += 2 {
		kf = append(kf, sec(float64(i)))
	}
	segs := PlanSegments(kf, sec(60))
	if len(segs) != 10 || segs[1].Start != sec(6) || segs[9].Start != sec(54) || segs[9].Duration != sec(6) {
		t.Errorf("regular keyframes: %v", segs)
	}
	// Sparse keyframes make long segments; none starts in the last second.
	segs = PlanSegments([]time.Duration{0, sec(1), sec(20), sec(29.5)}, sec(30))
	if len(segs) != 2 || segs[1].Start != sec(20) || segs[1].Duration != sec(10) {
		t.Errorf("sparse keyframes: %v", segs)
	}
	var total time.Duration
	for _, s := range segs {
		total += s.Duration
	}
	if total != sec(30) {
		t.Errorf("segments add up to %v", total)
	}
}

func TestPlaylist(t *testing.T) {
	pl := string(Playlist([]Segment{{0, sec(6)}, {sec(6), sec(7.5)}}, func(i int) string { return strconv.Itoa(i) + ".ts" }))
	for _, want := range []string{"#EXT-X-TARGETDURATION:8\n", "#EXTINF:6.000000,\n0.ts\n", "#EXTINF:7.500000,\n1.ts\n", "#EXT-X-ENDLIST\n"} {
		if !strings.Contains(pl, want) {
			t.Errorf("playlist lacks %q:\n%s", want, pl)
		}
	}
}

// testMovie makes 40 s of h264 with a keyframe every 2 s and AC3 audio.
func testMovie(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	path := filepath.Join(t.TempDir(), "movie.mkv")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=40", "-f", "lavfi", "-i", "sine=duration=40",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-bf", "2",
		"-c:a", "ac3", "-shortest", path).CombinedOutput()
	if err != nil {
		t.Skipf("cannot make the test movie: %v %s", err, out)
	}
	return ffmpeg, path
}

// firstVideoPTS is the earliest video timestamp of a segment.
func firstVideoPTS(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	lowest := -1.0
	for _, l := range strings.Fields(strings.ReplaceAll(string(out), ",", " ")) {
		if v, err := strconv.ParseFloat(l, 64); err == nil && (lowest < 0 || v < lowest) {
			lowest = v
		}
	}
	return lowest
}

func TestRemux(t *testing.T) {
	ffmpeg, path := testMovie(t)
	m := &Manager{FFmpeg: ffmpeg, Dir: filepath.Join(t.TempDir(), "hls"), Workers: 1,
		Keyframes: func(_ context.Context, s Source) ([]time.Duration, error) { return keyframes.Read(s.Path) }}
	src := Source{Key: "movie", Path: path, Duration: sec(40), Audio: 1}
	segs, err := m.Segments(context.Background(), src)
	if err != nil || len(segs) != 7 {
		t.Fatalf("segments %v, %v", segs, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// From the start, then a jump far ahead: both give the planned times
	// (plus the fixed offset), so the player sees one timeline.
	for _, n := range []int{0, 1, 5, 6} {
		p, err := m.Segment(ctx, src, n)
		if err != nil {
			t.Fatalf("segment %d: %v", n, err)
		}
		want := segs[n].Start.Seconds() + tsOffset.Seconds()
		if got := firstVideoPTS(t, p); got < want-0.01 || got > want+0.1 {
			t.Errorf("segment %d starts at %.3f, want %.3f", n, got, want)
		}
	}
	if _, err := m.Segment(ctx, src, 99); err == nil {
		t.Error("segment beyond the end")
	}
	// Idle sessions end and leave nothing behind.
	m.mu.Lock()
	for _, s := range m.sessions {
		s.seen = time.Now().Add(-idleTimeout - time.Second)
	}
	m.mu.Unlock()
	m.sweep()
	if m.Sessions() != 0 {
		t.Errorf("%d sessions after sweeping", m.Sessions())
	}
	if des, _ := os.ReadDir(m.Dir); len(des) != 0 {
		t.Errorf("left behind: %v", des)
	}
}

func TestBusy(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755)
	m := &Manager{FFmpeg: bin, Dir: filepath.Join(t.TempDir(), "hls"), Workers: 1,
		Keyframes: func(context.Context, Source) ([]time.Duration, error) { return []time.Duration{0, sec(10)}, nil }}
	a := Source{Key: "a", Path: "/m/a.mkv", Duration: sec(20), Audio: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := m.Segment(ctx, a, 0); err != context.DeadlineExceeded {
		t.Fatalf("waiting for a stuck ffmpeg: %v", err)
	}
	b := a
	b.Key = "b"
	if _, err := m.Segment(context.Background(), b, 0); err != ErrBusy {
		t.Errorf("second session: %v, want ErrBusy", err)
	}
	m.mu.Lock()
	for key, s := range m.sessions {
		m.end(key, s)
	}
	m.mu.Unlock()
}
