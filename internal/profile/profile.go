// Package profile decides, from a client's Jellyfin device profile,
// whether a file can be played directly, following the rules of Jellyfin's
// StreamBuilder.
package profile

import (
	"strconv"
	"strings"

	"homeflix/internal/media"
)

// DeviceProfile is the part of a Jellyfin device profile that decides
// direct play. Field names match Jellyfin's JSON.
type DeviceProfile struct {
	MaxStreamingBitrate int64
	MaxStaticBitrate    int64
	DirectPlayProfiles  []DirectPlayProfile
	CodecProfiles       []CodecProfile
	ContainerProfiles   []ContainerProfile
}

// DirectPlayProfile lists containers and codecs a client plays as they are.
type DirectPlayProfile struct {
	Type       string
	Container  string
	VideoCodec string
	AudioCodec string
}

// CodecProfile limits a codec; Conditions only apply when ApplyConditions
// hold.
type CodecProfile struct {
	Type            string // "Video", "VideoAudio" or "Audio"
	Codec           string
	Container       string
	Conditions      []Condition
	ApplyConditions []Condition
}

// ContainerProfile limits a container.
type ContainerProfile struct {
	Type       string
	Container  string
	Conditions []Condition
}

// Condition is one rule on a stream property.
type Condition struct {
	Condition  string // Equals, NotEquals, LessThanEqual, GreaterThanEqual, EqualsAny
	Property   string
	Value      string
	IsRequired bool
}

// Reasons Jellyfin reports when direct play is not possible.
const (
	ContainerNotSupported        = "ContainerNotSupported"
	VideoCodecNotSupported       = "VideoCodecNotSupported"
	AudioCodecNotSupported       = "AudioCodecNotSupported"
	ContainerBitrateExceedsLimit = "ContainerBitrateExceedsLimit"
)

// propertyReasons names the reason for a failed condition on a property.
var propertyReasons = map[string]string{
	"width": "VideoResolutionNotSupported", "height": "VideoResolutionNotSupported",
	"videobitdepth": "VideoBitDepthNotSupported", "videobitrate": "VideoBitrateNotSupported",
	"videoframerate": "VideoFramerateNotSupported", "videolevel": "VideoLevelNotSupported",
	"videoprofile": "VideoProfileNotSupported", "videorangetype": "VideoRangeTypeNotSupported",
	"videocodectag": "VideoCodecTagNotSupported", "isanamorphic": "AnamorphicVideoNotSupported",
	"isinterlaced": "InterlacedVideoNotSupported", "refframes": "RefFramesNotSupported",
	"isavc": "VideoCodecNotSupported", "numaudiostreams": "AudioCodecNotSupported",
	"numvideostreams": "VideoCodecNotSupported",
	"audiochannels":   "AudioChannelsNotSupported", "audiobitrate": "AudioBitrateNotSupported",
	"audioprofile": "AudioProfileNotSupported", "audiosamplerate": "AudioSampleRateNotSupported",
	"audiobitdepth": "AudioBitDepthNotSupported", "issecondaryaudio": "SecondaryAudioNotSupported",
}

// inList reports whether a comma-separated profile list is empty (meaning
// any) or contains one of the comma-separated values.
func inList(list, values string) bool {
	if strings.TrimSpace(list) == "" {
		return true
	}
	for _, v := range strings.Split(values, ",") {
		v = strings.TrimSpace(v)
		for _, l := range strings.Split(list, ",") {
			if v != "" && strings.EqualFold(strings.TrimSpace(l), v) {
				return true
			}
		}
	}
	return false
}

// Decision is the outcome for one media source.
type Decision struct {
	DirectPlay bool
	Reasons    []string // Jellyfin transcode reasons, empty when DirectPlay
}

// Evaluate decides whether the client can play info directly with the
// audio stream at audioIndex (-1 for the default). maxBitrate is the
// client's limit for this request, 0 for none.
func (p DeviceProfile) Evaluate(info media.Info, audioIndex int, bitrate, maxBitrate int64) Decision {
	video, hasVideo := info.Video()
	var audio media.Stream
	hasAudio := false
	if audioIndex < 0 {
		audioIndex = info.DefaultAudioIndex()
	}
	numAudio := 0
	for _, s := range info.Streams {
		if s.Type == "Audio" {
			numAudio++
			if s.Index == audioIndex {
				audio, hasAudio = s, true
			}
		}
	}
	var reasons []string
	add := func(r string) {
		for _, have := range reasons {
			if have == r {
				return
			}
		}
		reasons = append(reasons, r)
	}

	limit := maxBitrate
	if p.MaxStaticBitrate > 0 && (limit == 0 || p.MaxStaticBitrate < limit) {
		limit = p.MaxStaticBitrate
	}
	if limit > 0 && bitrate > limit {
		add(ContainerBitrateExceedsLimit)
	}

	containerOK, videoOK, audioOK := false, false, false
	for _, dp := range p.DirectPlayProfiles {
		if !strings.EqualFold(dp.Type, "Video") {
			continue
		}
		c := inList(dp.Container, info.Container)
		v := !hasVideo || inList(dp.VideoCodec, video.Codec)
		a := !hasAudio || inList(dp.AudioCodec, audio.Codec)
		containerOK = containerOK || c
		videoOK = videoOK || (c && v)
		audioOK = audioOK || (c && v && a)
	}
	switch {
	case !containerOK:
		add(ContainerNotSupported)
	case !videoOK:
		add(VideoCodecNotSupported)
	case !audioOK:
		add(AudioCodecNotSupported)
	}

	props := properties(video, hasVideo, audio, hasAudio, numAudio)
	for _, cp := range p.ContainerProfiles {
		if strings.EqualFold(cp.Type, "Video") && inList(cp.Container, info.Container) {
			for _, r := range failed(cp.Conditions, props) {
				add(r)
			}
		}
	}
	for _, cp := range p.CodecProfiles {
		var codec string
		switch {
		case strings.EqualFold(cp.Type, "Video") && hasVideo:
			codec = video.Codec
		case strings.EqualFold(cp.Type, "VideoAudio") && hasAudio:
			codec = audio.Codec
		default:
			continue
		}
		if !inList(cp.Codec, codec) || !inList(cp.Container, info.Container) {
			continue
		}
		if len(failed(cp.ApplyConditions, props)) > 0 {
			continue
		}
		for _, r := range failed(cp.Conditions, props) {
			add(r)
		}
	}
	return Decision{DirectPlay: len(reasons) == 0, Reasons: reasons}
}

// properties are the stream values conditions are evaluated against; a
// missing key means unknown.
func properties(v media.Stream, hasVideo bool, a media.Stream, hasAudio bool, numAudio int) map[string]string {
	p := map[string]string{"numaudiostreams": strconv.Itoa(numAudio)}
	set := func(k string, n int) {
		if n > 0 {
			p[k] = strconv.Itoa(n)
		}
	}
	if hasVideo {
		p["numvideostreams"] = "1"
		set("width", v.Width)
		set("height", v.Height)
		set("videobitdepth", v.BitDepth)
		set("refframes", v.RefFrames)
		if v.BitRate > 0 {
			p["videobitrate"] = strconv.FormatInt(v.BitRate, 10)
		}
		if f := v.AverageFrameRate; f > 0 && f < 1000 {
			p["videoframerate"] = strconv.FormatFloat(f, 'f', -1, 64)
		} else if v.RealFrameRate > 0 {
			p["videoframerate"] = strconv.FormatFloat(v.RealFrameRate, 'f', -1, 64)
		}
		if v.Level > 0 {
			p["videolevel"] = strconv.FormatFloat(v.Level, 'f', -1, 64)
		}
		if v.Profile != "" {
			p["videoprofile"] = v.Profile
		}
		if v.VideoRangeType != "" {
			p["videorangetype"] = v.VideoRangeType
		}
		if v.CodecTag != "" {
			p["videocodectag"] = v.CodecTag
		}
		p["isanamorphic"] = strconv.FormatBool(v.IsAnamorphic)
		p["isinterlaced"] = strconv.FormatBool(v.IsInterlaced)
		if v.IsAVC != nil {
			p["isavc"] = strconv.FormatBool(*v.IsAVC)
		}
	}
	if hasAudio {
		set("audiochannels", a.Channels)
		set("audiosamplerate", a.SampleRate)
		set("audiobitdepth", a.BitDepth)
		if a.BitRate > 0 {
			p["audiobitrate"] = strconv.FormatInt(a.BitRate, 10)
		}
		if a.Profile != "" {
			p["audioprofile"] = a.Profile
		}
		p["issecondaryaudio"] = "false"
	}
	return p
}

// failed returns the reasons of the conditions that do not hold.
func failed(conds []Condition, props map[string]string) []string {
	var out []string
	for _, c := range conds {
		prop := strings.ToLower(c.Property)
		value, known := props[prop]
		ok := !c.IsRequired
		if known {
			ok = holds(c, value)
		}
		if !ok {
			r := propertyReasons[prop]
			if r == "" {
				r = "UnknownProperty"
			}
			out = append(out, r)
		}
	}
	return out
}

func holds(c Condition, actual string) bool {
	a, aErr := strconv.ParseFloat(actual, 64)
	want, wErr := strconv.ParseFloat(c.Value, 64)
	numeric := aErr == nil && wErr == nil
	switch strings.ToLower(c.Condition) {
	case "equals":
		if numeric {
			return a == want
		}
		return strings.EqualFold(actual, c.Value)
	case "notequals":
		if numeric {
			return a != want
		}
		return !strings.EqualFold(actual, c.Value)
	case "lessthanequal":
		return numeric && a <= want
	case "greaterthanequal":
		return numeric && a >= want
	case "equalsany":
		for _, v := range strings.Split(c.Value, "|") {
			if n, err := strconv.ParseFloat(v, 64); err == nil && aErr == nil {
				if n == a {
					return true
				}
			} else if strings.EqualFold(strings.TrimSpace(v), actual) {
				return true
			}
		}
		return false
	}
	// Conditions this server does not know are not held against a file.
	return true
}
