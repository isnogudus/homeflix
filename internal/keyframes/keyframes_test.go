package keyframes

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The test files are 10 s of 25 fps h264 with B-frames and a keyframe every
// 2 s, written by ffmpeg (Matroska, MP4 with the moov box first and last)
// and by mkvmerge. ffprobe finds keyframes at 0, 2, 4, 6 and 8 s in each.
// allcues.mkv is ffmpeg.mkv remuxed with mkvmerge --cues 0:all: its Cues
// list every frame.
func TestRead(t *testing.T) {
	want := []time.Duration{0, 2 * time.Second, 4 * time.Second, 6 * time.Second, 8 * time.Second}
	for _, name := range []string{"ffmpeg.mkv", "mkvmerge.mkv", "faststart.mp4", "moovlast.mp4"} {
		t.Run(name, func(t *testing.T) {
			got, err := Read(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("keyframes %v, want %v", got, want)
			}
			for i := range want {
				// MP4 times are exact; Matroska rounds to its timecode scale.
				if d := got[i] - want[i]; d < -time.Millisecond || d > time.Millisecond {
					t.Errorf("keyframe %d at %v, want %v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestBrokenFiles(t *testing.T) {
	mkv, _ := os.ReadFile(filepath.Join("testdata", "ffmpeg.mkv"))
	mp4, _ := os.ReadFile(filepath.Join("testdata", "moovlast.mp4"))
	for name, data := range map[string][]byte{
		"empty":         {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		"text":          []byte("not a video file at all"),
		"truncated mkv": mkv[:len(mkv)/3],
		"truncated mp4": mp4[:len(mp4)/2],
	} {
		if _, err := FromReader(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Errorf("%s: no error", name)
		} else if name == "text" && !errors.Is(err, ErrNoIndex) {
			t.Errorf("%s: %v, want ErrNoIndex", name, err)
		}
	}
}
