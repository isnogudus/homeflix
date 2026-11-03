package images

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
)

func TestTargetSize(t *testing.T) {
	for _, tc := range []struct {
		w, h         int
		s            Size
		aspect       float64
		wantW, wantH int
	}{
		{1280, 720, Size{}, 0, 1280, 720},
		{1280, 720, Size{MaxWidth: 640}, 0, 640, 360},
		{1280, 720, Size{MaxHeight: 180}, 0, 320, 180},
		{1280, 720, Size{MaxWidth: 4000}, 0, 1280, 720},
		{1280, 720, Size{Width: 300, MaxWidth: 600}, 0, 300, 169},
		{480, 720, Size{FillWidth: 240, FillHeight: 360}, 240.0 / 360.0, 240, 360},
		{1280, 720, Size{FillWidth: 400, FillHeight: 400}, 1, 400, 400},
		{1280, 720, Size{FillHeight: 360}, 0, 640, 360},
	} {
		aspect, w, h := targetSize(tc.w, tc.h, tc.s)
		if aspect != tc.aspect || w != tc.wantW || h != tc.wantH {
			t.Errorf("targetSize(%d, %d, %+v) = %v, %d, %d; want %v, %d, %d", tc.w, tc.h, tc.s, aspect, w, h, tc.aspect, tc.wantW, tc.wantH)
		}
	}
}

func TestCropToAspect(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	if b := cropToAspect(img, PosterAspect).Bounds(); b.Dx() != 480 || b.Dy() != 720 {
		t.Errorf("poster crop %v", b)
	}
	tall := image.NewRGBA(image.Rect(0, 0, 400, 1000))
	if b := cropToAspect(tall, PosterAspect).Bounds(); b.Dx() != 400 || b.Dy() != 600 {
		t.Errorf("tall crop %v", b)
	}
}

func TestSeekPositions(t *testing.T) {
	got := seekPositions(100 * time.Minute)
	if len(got) != 3 || got[0] != 10*time.Minute || got[2] != 50*time.Minute {
		t.Errorf("positions %v", got)
	}
	if got := seekPositions(0); len(got) != 1 || got[0] != time.Minute {
		t.Errorf("unknown duration: %v", got)
	}
}

// frame is a 1280×720 test picture: a gradient with black bars of height
// bar above and below.
func frame(bar int, bright uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if y >= bar && y < 720-bar {
				c = color.RGBA{uint8(x / 5), bright, uint8(y / 3), 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// fakeFFmpeg writes a script that copies a prepared PNG to every output
// argument and counts its runs.
func fakeFFmpeg(t *testing.T, img image.Image) (bin string, runs func() int) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "frame.png")
	f, _ := os.Create(src)
	png.Encode(f, img)
	f.Close()
	counter := filepath.Join(dir, "runs")
	bin = filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\necho x >> " + counter + "\nsleep 0.2\nfor a; do case $a in *.png) cp " + src + " \"$a\";; esac; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "x")
	}
}

func testMovie(extra bool) *catalog.Movie {
	probe := `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"duration":"5400"}}`
	items := []store.Item{{ID: "m1", LibraryID: "lib", Type: "Movie", Title: "A", SortTitle: "a",
		Files: []store.MediaFile{{ID: "f1", ItemID: "m1", Path: "/m/A.mkv", Size: 10, Probe: probe}}}}
	if extra {
		items = append(items, store.Item{ID: "x1", LibraryID: "lib", ParentID: "m1", Type: "Video", Title: "X", SortTitle: "x",
			Files: []store.MediaFile{{ID: "f2", ItemID: "x1", Path: "/m/X.mkv", Size: 5, Probe: probe}}})
	}
	snap := catalog.Build(nil, items, "en", nil)
	id := "m1"
	if extra {
		id = "x1"
	}
	m, _ := snap.Item(id)
	return m
}

func TestGetExtractsOnceTrimsAndScales(t *testing.T) {
	bin, runs := fakeFFmpeg(t, frame(90, 128))
	s := &Service{FFmpeg: bin, Dir: t.TempDir(), Workers: 2}
	m := testMovie(false)

	var wg sync.WaitGroup
	paths := make([]string, 4)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.Get(context.Background(), m, Primary, Size{MaxHeight: 270})
			if err != nil {
				t.Error(err)
			}
			paths[i] = p
		}()
	}
	wg.Wait()
	if runs() != 1 {
		t.Errorf("ffmpeg ran %d times for concurrent requests, want 1", runs())
	}
	// The still is the frame without its 90-pixel bars: 1280×540.
	still, err := decodeFile(s.stillPath(stillTag(m)))
	if err != nil || still.Bounds().Dx() != 1280 || still.Bounds().Dy() != 540 {
		t.Fatalf("still %v, %v; want 1280x540", still.Bounds(), err)
	}
	poster, _ := decodeFile(paths[0])
	if b := poster.Bounds(); b.Dx() != 180 || b.Dy() != 270 {
		t.Errorf("poster size %v, want 180x270", b)
	}
	backdrop, err := s.Get(context.Background(), m, Backdrop, Size{MaxWidth: 640})
	if err != nil {
		t.Fatal(err)
	}
	if img, _ := decodeFile(backdrop); img.Bounds().Dx() != 640 || img.Bounds().Dy() != 270 {
		t.Errorf("backdrop size %v", img.Bounds())
	}
	if runs() != 1 {
		t.Errorf("backdrop extracted the still again")
	}
	if Tag(m, Primary) == Tag(m, Backdrop) || Tag(m, Thumb) != Tag(m, Backdrop) || len(Tag(m, Primary)) != 16 {
		t.Errorf("tags: %q %q %q", Tag(m, Primary), Tag(m, Backdrop), Tag(m, Thumb))
	}
}

func TestWarmRemovesUnusedImages(t *testing.T) {
	bin, runs := fakeFFmpeg(t, frame(0, 128))
	dir := t.TempDir()
	s := &Service{FFmpeg: bin, Dir: dir, Workers: 1}
	for _, p := range []string{"stills/0123456789abcdef.jpg", "scaled/0123456789abcdef/x.jpg", "scaled/flat.jpg"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte("old"), 0o644)
	}
	m := testMovie(true).Parent
	s.Warm(context.Background(), []*catalog.Movie{m})
	if runs() != 2 {
		t.Errorf("ffmpeg ran %d times, want 2 (movie and extra)", runs())
	}
	for _, p := range []string{"stills/0123456789abcdef.jpg", "scaled/0123456789abcdef", "scaled/flat.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("%s not removed", p)
		}
	}
	if _, err := os.Stat(s.stillPath(stillTag(m))); err != nil {
		t.Error("current still missing")
	}
	s.Warm(context.Background(), []*catalog.Movie{m})
	if runs() != 2 {
		t.Error("second warm-up extracted again")
	}
}

func TestExtraPrimaryKeepsFrameAspect(t *testing.T) {
	bin, _ := fakeFFmpeg(t, frame(0, 128))
	s := &Service{FFmpeg: bin, Dir: t.TempDir(), Workers: 1}
	x := testMovie(true)
	p, err := s.Get(context.Background(), x, Primary, Size{MaxWidth: 320})
	if err != nil {
		t.Fatal(err)
	}
	if img, _ := decodeFile(p); img.Bounds().Dx() != 320 || img.Bounds().Dy() != 180 {
		t.Errorf("extra primary %v, want 320x180", img.Bounds())
	}
	if a := AspectRatio(x); a < 1.77 || a > 1.78 {
		t.Errorf("extra aspect %v", a)
	}
	if AspectRatio(testMovie(false)) != PosterAspect {
		t.Error("movie aspect is not the poster aspect")
	}
}

func TestExtractionFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	os.WriteFile(bin, []byte("#!/bin/sh\necho 'Invalid data found' >&2\nexit 1\n"), 0o755)
	s := &Service{FFmpeg: bin, Dir: t.TempDir(), Workers: 1}
	_, err := s.Get(context.Background(), testMovie(false), Primary, Size{})
	if err == nil || !strings.Contains(err.Error(), "Invalid data found") {
		t.Errorf("err = %v", err)
	}
}
