package subtitles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"homeflix/internal/catalog"
	"homeflix/internal/media"
	"homeflix/internal/store"
)

func part() catalog.Part {
	return catalog.Part{
		File: store.MediaFile{ID: "f1", Path: "/m/A.mkv", Size: 10},
		Info: media.Info{Streams: []media.Stream{
			{Index: 0, Type: "Video", Codec: "h264"},
			{Index: 3, Type: "Subtitle", Codec: "PGSSUB"},
			{Index: 4, Type: "Subtitle", Codec: "subrip", IsTextSubtitleStream: true},
		}},
	}
}

// fakeFFmpeg writes its arguments, one per line, to the output file (the
// last argument) and counts its runs.
func fakeFFmpeg(t *testing.T) (string, func() int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	bin := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\necho x >> " + counter + "\nsleep 0.2\nfor a; do last=$a; done\nfor a; do echo \"$a\"; done > \"$last\"\n"
	os.WriteFile(bin, []byte(script), 0o755)
	return bin, func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "x")
	}
}

func TestGet(t *testing.T) {
	bin, runs := fakeFFmpeg(t)
	s := &Service{FFmpeg: bin, Dir: t.TempDir()}
	var wg sync.WaitGroup
	paths := make([]string, 3)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.Get(context.Background(), part(), 4, "VTT")
			if err != nil {
				t.Error(err)
			}
			paths[i] = p
		}()
	}
	wg.Wait()
	if runs() != 1 {
		t.Errorf("ffmpeg ran %d times for concurrent requests", runs())
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	args := string(b)
	for _, want := range []string{"file:/m/A.mkv", "\n0:4\n", "\nwebvtt\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg arguments lack %q:\n%s", want, args)
		}
	}
	if _, err := s.Get(context.Background(), part(), 4, "vtt"); err != nil || runs() != 1 {
		t.Errorf("cached file extracted again: %v, %d runs", err, runs())
	}
	if _, err := s.Get(context.Background(), part(), 3, "srt"); !errors.Is(err, ErrNotText) {
		t.Errorf("PGS: %v", err)
	}
	if _, err := s.Get(context.Background(), part(), 9, "srt"); !errors.Is(err, ErrNotText) {
		t.Errorf("unknown stream: %v", err)
	}
	if _, err := s.Get(context.Background(), part(), 4, "sub"); !errors.Is(err, ErrFormat) {
		t.Errorf("unknown format: %v", err)
	}
	if ContentType("SRT") == "" || ContentType("x") != "" {
		t.Error("content types")
	}
}

func TestRemoveUnused(t *testing.T) {
	bin, _ := fakeFFmpeg(t)
	s := &Service{FFmpeg: bin, Dir: t.TempDir()}
	p := part()
	path, err := s.Get(context.Background(), p, 4, "srt")
	if err != nil {
		t.Fatal(err)
	}
	movie := &catalog.Movie{Versions: []*catalog.Version{{Parts: []catalog.Part{p}}}}
	s.RemoveUnused([]*catalog.Movie{movie})
	if _, err := os.Stat(path); err != nil {
		t.Errorf("subtitles of a present file removed: %v", err)
	}
	// The file changed: its old subtitles are stale.
	movie.Versions[0].Parts[0].File.Size++
	s.RemoveUnused([]*catalog.Movie{movie})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale subtitles kept: %v", err)
	}
}
