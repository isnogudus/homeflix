package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	out := []byte(`{
  "streams": [ {"index": 0, "codec_type": "video", "codec_name": "h264", "refs": 5} ],
  "chapters": [],
  "format": {"format_name": "matroska,webm", "duration": "7543.210000", "bit_rate": "21000000"}
}`)
	r := parse(out)
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	if r.Container != "matroska,webm" || r.Duration != 7543210*time.Millisecond || r.BitRate != 21000000 {
		t.Errorf("summary: %+v", r)
	}
	if strings.Contains(r.JSON, "\n") || !strings.Contains(r.JSON, `"refs":5`) {
		t.Errorf("JSON not compacted or incomplete: %s", r.JSON)
	}
	if r := parse([]byte(`{"streams":[],"format":{}}`)); r.Err == "" {
		t.Error("no streams: want error")
	}
	if r := parse([]byte(`garbage`)); r.Err == "" {
		t.Error("garbage: want error")
	}
}

// fakeProbe writes a shell script standing in for ffprobe.
func fakeProbe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeRunsBinary(t *testing.T) {
	ok := fakeProbe(t, `for a; do last=$a; done
printf '{"streams":[{"index":0}],"format":{"format_name":"mov,mp4","duration":"1.5","filename":"%s"}}' "$last"`)
	r := FFprobe{Path: ok}.Probe(context.Background(), "/m/A (2000).mp4")
	if r.Err != "" || r.Container != "mov,mp4" || r.Duration != 1500*time.Millisecond {
		t.Fatalf("result %+v", r)
	}
	if !strings.Contains(r.JSON, `file:/m/A (2000).mp4`) {
		t.Errorf("path not passed with file: prefix: %s", r.JSON)
	}

	failing := fakeProbe(t, `echo "Invalid data found when processing input" >&2; exit 1`)
	if r := (FFprobe{Path: failing}).Probe(context.Background(), "/x.mkv"); r.Err != "Invalid data found when processing input" {
		t.Errorf("error = %q", r.Err)
	}

	if _, err := exec.LookPath("sleep"); err == nil {
		slow := fakeProbe(t, `exec sleep 5`)
		r := FFprobe{Path: slow, Timeout: 100 * time.Millisecond}.Probe(context.Background(), "/x.mkv")
		if !strings.Contains(r.Err, "timed out") {
			t.Errorf("slow probe: %+v", r)
		}
	}

	if r := (FFprobe{Path: "/nonexistent/ffprobe"}).Probe(context.Background(), "/x.mkv"); r.Err == "" {
		t.Error("missing binary: want error")
	}
}
