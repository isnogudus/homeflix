package media

import (
	"os"
	"testing"
	"time"
)

// The fixtures are unmodified ffprobe output (-show_format -show_streams
// -show_chapters -show_data) of files from the library homeflix was built
// for.
func load(t *testing.T, name string, loc Locale) Info {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := Parse(string(b), loc)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func stream(t *testing.T, info Info, index int) Stream {
	t.Helper()
	for _, s := range info.Streams {
		if s.Index == index {
			return s
		}
	}
	t.Fatalf("no stream %d", index)
	return Stream{}
}

func TestParseMatroskaHDR10(t *testing.T) {
	info := load(t, "arrival", "en")
	if info.Container != "mkv" || info.Duration.Round(time.Second) != 116*time.Minute+32*time.Second || len(info.Chapters) != 16 {
		t.Errorf("format: %q %s %d chapters", info.Container, info.Duration, len(info.Chapters))
	}
	v := stream(t, info, 0)
	if v.Type != "Video" || v.Codec != "hevc" || v.BitDepth != 10 || v.VideoRange != "HDR" || v.VideoRangeType != "HDR10" ||
		v.Width != 3840 || v.DisplayTitle != "4K HEVC HDR" {
		t.Errorf("video: %+v", v)
	}
	dts := stream(t, info, 2)
	if dts.Codec != "dts" || dts.Profile != "DTS-HD MA" || dts.ChannelLayout != "7.1" || dts.Language != "eng" ||
		dts.IsDefault || dts.DisplayTitle != "English 7.1 (DTS-HD MA)" {
		t.Errorf("dts: %+v", dts)
	}
	sub := stream(t, info, 3)
	if sub.Codec != "PGSSUB" || sub.IsTextSubtitleStream || sub.SupportsExternalStream || !sub.IsDefault ||
		sub.DisplayTitle != "Deutsch (Forced) - German - Default - PGSSUB" {
		t.Errorf("subtitle: %+v", sub)
	}
	if info.DefaultAudioIndex() != 1 || info.DefaultSubtitleIndex() != 3 {
		t.Errorf("defaults: audio %d, subtitle %d", info.DefaultAudioIndex(), info.DefaultSubtitleIndex())
	}
}

func TestParseMP4H264(t *testing.T) {
	info := load(t, "twelve-years", "en")
	if info.Container != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("container %q", info.Container)
	}
	v := stream(t, info, 0)
	if v.RefFrames != 4 || v.Level != 40 || v.IsAVC == nil || !*v.IsAVC || v.BitDepth != 8 ||
		v.VideoRangeType != "SDR" || v.DisplayTitle != "1080p H264 SDR" || v.AverageFrameRate < 23.97 || v.AverageFrameRate > 25.01 {
		t.Errorf("video: %+v", v)
	}
	aac := stream(t, info, 1)
	if aac.Profile != "LC" || aac.ChannelLayout != "stereo" || aac.DisplayTitle != "German - AAC - Stereo - Default" {
		t.Errorf("aac: %+v", aac)
	}
	if info.DefaultSubtitleIndex() != -1 {
		t.Errorf("default subtitle %d, want -1", info.DefaultSubtitleIndex())
	}
}

func TestParseDolbyVisionProfile7(t *testing.T) {
	info := load(t, "dune", "en")
	v := stream(t, info, 0)
	if v.DV == nil || v.DV.Profile != 7 || v.DV.ELPresent != 1 || v.DV.BLSignalCompatibilityID != 6 {
		t.Fatalf("dolby vision: %+v", v.DV)
	}
	if v.VideoRange != "HDR" || v.VideoRangeType != "DOVIWithEL" || v.DisplayTitle != "4K HEVC Dolby Vision Profile 7.6 (HDR10)" {
		t.Errorf("video: range %s/%s, title %q", v.VideoRange, v.VideoRangeType, v.DisplayTitle)
	}
	atmos := stream(t, info, 2)
	if atmos.AudioSpatialFormat != "DolbyAtmos" || atmos.Codec != "truehd" || atmos.BitDepth != 24 {
		t.Errorf("atmos: %+v", atmos)
	}
	if e := stream(t, info, 1); e.DisplayTitle != "Deutsch 5.1 (E-AC3 640k, Kompatibilität) - German - Dolby Digital+ - Default" {
		t.Errorf("eac3 title %q", e.DisplayTitle)
	}
}

func TestParseGermanLocale(t *testing.T) {
	info := load(t, "arrival", "de")
	if got := stream(t, info, 1).DisplayTitle; got != "Deutsch 5.1 (AC3) - Dolby Digital - Standard" {
		t.Errorf("audio title %q", got)
	}
	if got := stream(t, info, 3).DisplayTitle; got != "Deutsch (Forced) - Standard - Erzwungen - PGSSUB" {
		t.Errorf("subtitle title %q", got)
	}
	if got := stream(t, info, 2).DisplayTitle; got != "English 7.1 (DTS-HD MA) - Englisch" {
		t.Errorf("english audio title %q", got)
	}
}

func TestVideoRangeRules(t *testing.T) {
	bt2020 := Stream{ColorSpace: "bt2020nc", ColorPrimaries: "bt2020"}
	tests := []struct {
		name     string
		s        Stream
		rng, typ string
	}{
		{"sdr", Stream{}, "SDR", "SDR"},
		{"hlg", Stream{ColorTransfer: "arib-std-b67"}, "HDR", "HLG"},
		{"profile 5", Stream{DV: &DolbyVision{Profile: 5, RPUPresent: 1, BLPresent: 1}}, "HDR", "DOVI"},
		{"profile 8.1", with(bt2020, "smpte2084", &DolbyVision{Profile: 8, RPUPresent: 1, BLPresent: 1, BLSignalCompatibilityID: 1}), "HDR", "DOVIWithHDR10"},
		{"profile 8.1 without bt2020", Stream{ColorTransfer: "smpte2084", DV: &DolbyVision{Profile: 8, RPUPresent: 1, BLPresent: 1, BLSignalCompatibilityID: 1}}, "HDR", "DOVIInvalid"},
		{"profile 8.4", with(bt2020, "arib-std-b67", &DolbyVision{Profile: 8, RPUPresent: 1, BLPresent: 1, BLSignalCompatibilityID: 4}), "HDR", "DOVIWithHLG"},
		{"profile 8.2", Stream{DV: &DolbyVision{Profile: 8, RPUPresent: 1, BLPresent: 1, BLSignalCompatibilityID: 2}}, "SDR", "DOVIWithSDR"},
		{"no rpu falls back to hdr10", with(bt2020, "smpte2084", &DolbyVision{Profile: 8, BLPresent: 1, BLSignalCompatibilityID: 1}), "HDR", "HDR10"},
		{"dvh1 tag", Stream{CodecTag: "dvh1"}, "SDR", "SDR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rng, typ := videoRange(tc.s); rng != tc.rng || typ != tc.typ {
				t.Errorf("got %s/%s, want %s/%s", rng, typ, tc.rng, tc.typ)
			}
		})
	}
}

func with(s Stream, transfer string, dv *DolbyVision) Stream {
	s.ColorTransfer, s.DV = transfer, dv
	return s
}

func TestNormalizeContainer(t *testing.T) {
	vp9 := []Stream{{Type: "Video", Codec: "vp9"}, {Type: "Audio", Codec: "opus"}}
	h264 := []Stream{{Type: "Video", Codec: "h264"}, {Type: "Audio", Codec: "aac"}}
	for _, tc := range []struct {
		format  string
		streams []Stream
		want    string
	}{
		{"matroska,webm", vp9, "mkv,webm"},
		{"matroska,webm", h264, "mkv"},
		{"mpegts", h264, "ts"},
		{"mpegvideo", h264, "mpeg"},
		{"avi", h264, "avi"},
		{"", h264, ""},
	} {
		if got := normalizeContainer(tc.format, tc.streams); got != tc.want {
			t.Errorf("normalizeContainer(%q) = %q, want %q", tc.format, got, tc.want)
		}
	}
}
