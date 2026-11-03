package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeflix/internal/media"
)

// moonfin27 is the PlaybackInfo request Moonfin 2.7 sent from macOS, as
// recorded while evaluating another server: one direct play profile and the
// RefFrames <= 4 rule for h264 at 1900 px and more.
func moonfin27(t *testing.T) DeviceProfile {
	t.Helper()
	b, err := os.ReadFile("testdata/moonfin-2.7-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var req struct{ DeviceProfile DeviceProfile }
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatal(err)
	}
	return req.DeviceProfile
}

func fixture(t *testing.T, name string) media.Info {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "media", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := media.Parse(string(b), "en")
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestMoonfinProfileDirectPlaysLibraryFiles(t *testing.T) {
	p := moonfin27(t)
	if p.MaxStreamingBitrate != 120000000 || len(p.DirectPlayProfiles) != 1 || len(p.CodecProfiles) != 1 {
		t.Fatalf("profile not decoded: %+v", p)
	}
	for _, tc := range []struct {
		file  string
		audio int
	}{
		{"twelve-years", -1}, // h264 1080p with 4 reference frames, AAC
		{"arrival", 2},       // HEVC in Matroska with DTS-HD MA
		{"dune", 2},          // Dolby Vision with TrueHD Atmos
	} {
		info := fixture(t, tc.file)
		if d := p.Evaluate(info, tc.audio, info.BitRate, 0); !d.DirectPlay {
			t.Errorf("%s: %v", tc.file, d.Reasons)
		}
	}
}

func TestRefFramesRule(t *testing.T) {
	p := moonfin27(t)
	info := fixture(t, "twelve-years")
	video := func() *media.Stream {
		for i := range info.Streams {
			if info.Streams[i].Type == "Video" {
				return &info.Streams[i]
			}
		}
		t.Fatal("no video")
		return nil
	}
	video().RefFrames = 5
	if d := p.Evaluate(info, -1, 0, 0); d.DirectPlay || d.Reasons[0] != "RefFramesNotSupported" {
		t.Errorf("5 reference frames: %+v", d)
	}
	// Unknown reference frames pass an optional rule but fail a required
	// one: that is how another server ended up transcoding every file.
	video().RefFrames = 0
	if d := p.Evaluate(info, -1, 0, 0); !d.DirectPlay {
		t.Errorf("unknown refs, optional rule: %+v", d)
	}
	p.CodecProfiles[0].Conditions[0].IsRequired = true
	if d := p.Evaluate(info, -1, 0, 0); d.DirectPlay {
		t.Error("unknown refs, required rule: direct play allowed")
	}
	// The rule applies at 1900 px and more only.
	video().Width = 1280
	if d := p.Evaluate(info, -1, 0, 0); !d.DirectPlay {
		t.Errorf("720p: %+v", d)
	}
}

func TestReasons(t *testing.T) {
	p := moonfin27(t)
	arrival := fixture(t, "arrival")

	avi := arrival
	avi.Container = "avi"
	if d := p.Evaluate(avi, -1, 0, 0); d.DirectPlay || strings.Join(d.Reasons, ",") != ContainerNotSupported {
		t.Errorf("avi: %+v", d)
	}
	if d := p.Evaluate(arrival, -1, 90_000_000, 60_000_000); d.DirectPlay || d.Reasons[0] != ContainerBitrateExceedsLimit {
		t.Errorf("bitrate limit: %+v", d)
	}
	p.DirectPlayProfiles[0].AudioCodec = "aac,ac3"
	if d := p.Evaluate(arrival, 2, 0, 0); d.DirectPlay || d.Reasons[0] != AudioCodecNotSupported {
		t.Errorf("dts without dts support: %+v", d)
	}
	if d := p.Evaluate(arrival, 1, 0, 0); !d.DirectPlay {
		t.Errorf("ac3 track: %+v", d)
	}

	hdr := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{Type: "Video"}},
		CodecProfiles: []CodecProfile{{Type: "Video", Codec: "hevc", Conditions: []Condition{
			{Condition: "EqualsAny", Property: "VideoRangeType", Value: "SDR|HDR10|DOVIWithHDR10"},
		}}},
	}
	if d := hdr.Evaluate(arrival, -1, 0, 0); !d.DirectPlay {
		t.Errorf("HDR10 allowed: %+v", d)
	}
	if d := hdr.Evaluate(fixture(t, "dune"), -1, 0, 0); d.DirectPlay || d.Reasons[0] != "VideoRangeTypeNotSupported" {
		t.Errorf("Dolby Vision with EL: %+v", d)
	}
	if d := (DeviceProfile{}).Evaluate(arrival, -1, 0, 0); d.DirectPlay {
		t.Error("empty profile allows direct play")
	}
}

func TestConditions(t *testing.T) {
	for _, tc := range []struct {
		c      Condition
		actual string
		want   bool
	}{
		{Condition{Condition: "LessThanEqual", Value: "4"}, "4", true},
		{Condition{Condition: "LessThanEqual", Value: "4"}, "5", false},
		{Condition{Condition: "GreaterThanEqual", Value: "1900"}, "1920", true},
		{Condition{Condition: "Equals", Value: "true"}, "TRUE", true},
		{Condition{Condition: "NotEquals", Value: "High 10"}, "High", true},
		{Condition{Condition: "EqualsAny", Value: "high|main|baseline"}, "High", true},
		{Condition{Condition: "EqualsAny", Value: "150|153"}, "153", true},
		{Condition{Condition: "EqualsAny", Value: "150|153"}, "156", false},
		{Condition{Condition: "Frobnicate", Value: "x"}, "y", true},
	} {
		if got := holds(tc.c, tc.actual); got != tc.want {
			t.Errorf("%s %s on %q = %v, want %v", tc.c.Condition, tc.c.Value, tc.actual, got, tc.want)
		}
	}
}
