// Package hls converts files for browsers and serves them as HLS in MPEG-TS
// segments: the chosen audio track becomes AAC, and the video is either
// copied (remux) or transcoded to h264, with HDR mapped to SDR and an
// image subtitle burnt in if asked. Copied video can only be cut at
// keyframes, so its segments are planned from the keyframes of the file;
// transcoded video is cut at fixed times. ffmpeg makes the segments on
// demand, from where the player asks, and restarts when it jumps.
package hls

import (
	"bytes"
	"fmt"
	"math"
	"time"
)

// Segment is one piece of the playlist.
type Segment struct {
	Start, Duration time.Duration
}

// TargetDuration is the length segments are cut at, at the first keyframe
// from there on.
const TargetDuration = 6 * time.Second

// PlanSegments cuts a file of the given duration at keyframes, starting a
// segment at the first keyframe at least TargetDuration after the last
// start. The first segment starts at 0.
func PlanSegments(keyframes []time.Duration, total time.Duration) []Segment {
	starts := []time.Duration{0}
	for _, k := range keyframes {
		if k >= starts[len(starts)-1]+TargetDuration && k < total-time.Second {
			starts = append(starts, k)
		}
	}
	segs := make([]Segment, len(starts))
	for i, s := range starts {
		end := total
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		segs[i] = Segment{Start: s, Duration: end - s}
	}
	return segs
}

// Playlist writes the HLS media playlist of segs; uri names segment i.
func Playlist(segs []Segment, uri func(i int) string) []byte {
	var longest time.Duration
	for _, s := range segs {
		longest = max(longest, s.Duration)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:%d\n",
		int(math.Ceil(longest.Seconds())))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for i, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", s.Duration.Seconds(), uri(i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.Bytes()
}
