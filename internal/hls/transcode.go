package hls

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Video says how the video of a source is transcoded to h264. Sources
// without it have their video copied.
type Video struct {
	Width, Height int  // output size, from FitSize
	Deinterlace   bool // the source is interlaced
	ToneMap       bool // the source is HDR and is mapped to SDR
	Subtitle      int  // stream index of an image subtitle burnt in, -1 for none
}

// The largest picture transcodes make; larger video is scaled down.
const maxWidth, maxHeight = 1920, 1080

// FitSize is the output size for a video of w×h pixels: scaled down to
// fit 1920×1080 with its shape kept, in even numbers as h264 needs them.
func FitSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	f := min(1, float64(maxWidth)/float64(w), float64(maxHeight)/float64(h))
	even := func(x float64) int { return max(2, int(x/2+0.5)*2) }
	return even(float64(w) * f), even(float64(h) * f)
}

func (v *Video) key() string {
	if v == nil {
		return ""
	}
	k := fmt.Sprintf("-v%dx%d", v.Width, v.Height)
	if v.Subtitle >= 0 {
		k += "-s" + strconv.Itoa(v.Subtitle)
	}
	return k
}

// filter is the filter graph of a transcode; its output is [v].
//
// Interlaced video is deinterlaced first, frame for frame. HDR is scaled
// while still in 10 bits and then mapped to SDR in BT.709 by the scaler
// (perceptual intent), which costs a quarter of doing it at 4K. Image
// subtitles are scaled to the output size and laid over the picture.
func (v *Video) filter() string {
	var chain []string
	if v.Deinterlace {
		chain = append(chain, "bwdif=mode=send_frame:deint=interlaced")
	}
	chain = append(chain, fmt.Sprintf("scale=%d:%d", v.Width, v.Height))
	if v.ToneMap {
		chain = append(chain, "scale=out_transfer=bt709:out_primaries=bt709:out_color_matrix=bt709:intent=perceptual")
	}
	chain = append(chain, "format=yuv420p")
	if v.Subtitle < 0 {
		return "[0:v:0]" + strings.Join(chain, ",") + "[v]"
	}
	return fmt.Sprintf("[0:v:0]%s[main];[0:%d]scale=%d:%d[sub];[main][sub]overlay=eof_action=pass:repeatlast=0[v]",
		strings.Join(chain, ","), v.Subtitle, v.Width, v.Height)
}

// Encoder settings: fast enough for 4K HDR sources at twice real time on
// a 6-core desktop processor, at a quality for watching, not archiving.
var x264Args = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "21",
	"-maxrate", "10M", "-bufsize", "20M", "-profile:v", "high"}

// FixedSegments plans a transcode: it sets the keyframes itself, so every
// segment is TargetDuration long but the last.
func FixedSegments(total time.Duration) []Segment {
	var segs []Segment
	for start := time.Duration(0); start == 0 || start < total-time.Second; start += TargetDuration {
		segs = append(segs, Segment{Start: start, Duration: min(TargetDuration, total-start)})
	}
	if n := len(segs); segs[n-1].Start+segs[n-1].Duration < total {
		segs[n-1].Duration = total - segs[n-1].Start
	}
	return segs
}
