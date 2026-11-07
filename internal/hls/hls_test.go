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
	// Seeking back to a segment deleted behind the player makes it again,
	// also after a run from the same segment left its list behind.
	m.mu.Lock()
	for _, s := range m.sessions {
		s.lastReq = 6 + keepBehind + 1
	}
	m.mu.Unlock()
	m.sweep()
	for _, n := range []int{0, 0, 1} {
		p, err := m.Segment(ctx, src, n)
		if err != nil {
			t.Fatalf("segment %d again: %v", n, err)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("segment %d again: %v", n, err)
		}
	}
	// A player's new session ends its old one.
	m.Workers = 2
	other := src
	other.Owner, src.Owner = "p1", "p1"
	other.Key = "other"
	if _, err := m.Segment(ctx, src, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Segment(ctx, other, 0); err != nil {
		t.Fatal(err)
	}
	if all, _ := m.Sessions(); all != 2 {
		t.Errorf("%d sessions after switching audio, want the old one without owner and the new one", all)
	}
	// Idle sessions end and leave nothing behind.
	m.mu.Lock()
	for _, s := range m.sessions {
		s.seen = time.Now().Add(-idleTimeout - time.Second)
	}
	m.mu.Unlock()
	m.sweep()
	if all, _ := m.Sessions(); all != 0 {
		t.Errorf("%d sessions after sweeping", all)
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

	// Transcodes have a limit of their own within all workers.
	m.Workers, m.VideoWorkers = 3, 1
	v := a
	v.Video = &Video{Width: 160, Height: 120, Subtitle: -1}
	short := func(src Source) error {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, err := m.Segment(ctx, src, 0)
		return err
	}
	if err := short(v); err != context.DeadlineExceeded {
		t.Fatalf("first transcode: %v", err)
	}
	v2 := v
	v2.Key = "v2"
	if err := short(v2); err != ErrBusy {
		t.Errorf("second transcode: %v, want ErrBusy", err)
	}
	if err := short(b); err != context.DeadlineExceeded {
		t.Errorf("remux next to a transcode: %v", err)
	}
	if all, video := m.Sessions(); all != 2 || video != 1 {
		t.Errorf("sessions %d, video %d; want 2, 1", all, video)
	}
	// A transcode nobody asked for in a while makes room for a new one.
	m.mu.Lock()
	m.sessions[v.session()].seen = time.Now().Add(-yieldAfter - time.Second)
	m.mu.Unlock()
	if err := short(v2); err != context.DeadlineExceeded {
		t.Errorf("transcode after an idle one: %v", err)
	}
	m.mu.Lock()
	if r := m.sessions[v.session()].run; !r.exited() {
		t.Error("the idle transcode still runs")
	}
	m.mu.Unlock()
	m.mu.Lock()
	for key, s := range m.sessions {
		m.end(key, s)
	}
	m.mu.Unlock()
}

func TestFitSize(t *testing.T) {
	for _, c := range []struct{ w, h, ww, wh int }{
		{1920, 1080, 1920, 1080}, {3840, 2160, 1920, 1080}, {3840, 1600, 1920, 800},
		{720, 576, 720, 576}, {1440, 1080, 1440, 1080}, {4096, 1716, 1920, 804}, {0, 0, 0, 0},
	} {
		if w, h := FitSize(c.w, c.h); w != c.ww || h != c.wh {
			t.Errorf("FitSize(%d, %d) = %d, %d; want %d, %d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}

func TestFixedSegments(t *testing.T) {
	segs := FixedSegments(sec(60.5))
	if len(segs) != 10 || segs[1].Start != sec(6) || segs[9].Duration != sec(6.5) {
		t.Errorf("60.5 s: %v", segs)
	}
	if segs := FixedSegments(sec(6.5)); len(segs) != 1 || segs[0].Duration != sec(6.5) {
		t.Errorf("6.5 s: %v", segs)
	}
	if segs := FixedSegments(sec(9)); len(segs) != 2 || segs[1].Duration != sec(3) {
		t.Errorf("9 s: %v", segs)
	}
}

func TestVideoFilter(t *testing.T) {
	v := &Video{Width: 1920, Height: 800, ToneMap: true, Subtitle: -1}
	if f := v.filter(); f != "[0:v:0]scale=1920:800,scale=out_transfer=bt709:out_primaries=bt709:out_color_matrix=bt709:intent=perceptual,format=yuv420p[v]" {
		t.Errorf("HDR filter %q", f)
	}
	v = &Video{Width: 720, Height: 576, Deinterlace: true, Subtitle: 5}
	if f := v.filter(); f != "[0:v:0]bwdif=mode=send_frame:deint=interlaced,scale=720:576,format=yuv420p[main];[0:5]scale=720:576[sub];[main][sub]overlay=eof_action=pass:repeatlast=0[v]" {
		t.Errorf("subtitle filter %q", f)
	}
	if (*Video)(nil).key() != "" || v.key() != "-v720x576-s5" {
		t.Errorf("keys %q, %q", (*Video)(nil).key(), v.key())
	}
}

// keyframeTimes are the timestamps of the video keyframes of a segment.
func keyframeTimes(t *testing.T, path string) []float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var kf []float64
	for _, l := range strings.Fields(string(out)) {
		ts, flags, _ := strings.Cut(l, ",")
		if v, err := strconv.ParseFloat(ts, 64); err == nil && strings.HasPrefix(flags, "K") {
			kf = append(kf, v)
		}
	}
	return kf
}

func TestTranscode(t *testing.T) {
	ffmpeg, _ := testMovie(t)
	// 10-bit HDR, interlaced, with a second video stream standing in for
	// an image subtitle: the whole filter graph runs.
	path := filepath.Join(t.TempDir(), "hdr.mkv")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=30", "-f", "lavfi", "-i", "sine=duration=30",
		"-f", "lavfi", "-i", "color=c=white@0.5:size=320x240:rate=1:duration=30,format=yuva420p",
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v:0", "libx264", "-preset", "ultrafast", "-pix_fmt:v:0", "yuv420p10le", "-g", "250",
		"-color_trc", "smpte2084", "-color_primaries", "bt2020", "-colorspace", "bt2020nc", "-field_order", "tt",
		"-c:v:1", "ffv1", "-c:a", "ac3", path).CombinedOutput()
	if err != nil {
		t.Skipf("cannot make the HDR test movie: %v %s", err, out)
	}
	m := &Manager{FFmpeg: ffmpeg, Dir: filepath.Join(t.TempDir(), "hls"), Workers: 1, VideoWorkers: 1}
	src := Source{Key: "hdr", Path: path, Duration: sec(30), Audio: 1,
		Video: &Video{Width: 320, Height: 240, Deinterlace: true, ToneMap: true, Subtitle: 2}}
	segs, err := m.Segments(context.Background(), src)
	if err != nil || len(segs) != 5 {
		t.Fatalf("segments %v, %v", segs, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// From the start, then a jump: every segment starts with a keyframe at
	// its planned time, whichever run made it.
	for _, n := range []int{0, 1, 4, 3} {
		p, err := m.Segment(ctx, src, n)
		if err != nil {
			t.Fatalf("segment %d: %v", n, err)
		}
		want := segs[n].Start.Seconds() + tsOffset.Seconds()
		kf := keyframeTimes(t, p)
		if got := firstVideoPTS(t, p); got < want-0.01 || got > want+0.05 || len(kf) == 0 || kf[0] != got {
			t.Errorf("segment %d starts at %.3f with keyframes %v, want a keyframe at %.3f", n, got, kf, want)
		}
	}
	m.mu.Lock()
	for key, s := range m.sessions {
		m.end(key, s)
	}
	m.mu.Unlock()
}
