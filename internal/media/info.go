package media

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Info is what a probed file contains, described the way Jellyfin clients
// expect it.
type Info struct {
	Container string // Jellyfin's normalised form, e.g. "mkv" or "mov,mp4,m4a,3gp,3g2,mj2"
	Duration  time.Duration
	BitRate   int64
	Streams   []Stream
	Chapters  []Chapter
}

// Stream mirrors the fields of Jellyfin's MediaStream that clients use.
type Stream struct {
	Index    int
	Type     string // "Video", "Audio", "Subtitle", "EmbeddedImage" or "Data"
	Codec    string
	CodecTag string
	Profile  string
	Language string
	Title    string
	TimeBase string
	BitRate  int64

	IsDefault, IsForced, IsHearingImpaired, IsOriginal bool

	Width, Height                   int
	Level                           float64
	RefFrames                       int // 0 if unknown
	BitDepth                        int
	IsInterlaced                    bool
	IsAVC                           *bool
	NalLengthSize                   string
	AspectRatio                     string
	IsAnamorphic                    bool
	PixelFormat                     string
	ColorSpace                      string
	ColorTransfer                   string
	ColorPrimaries                  string
	AverageFrameRate, RealFrameRate float64
	VideoRange, VideoRangeType      string
	DV                              *DolbyVision
	Channels, SampleRate            int
	ChannelLayout                   string
	AudioSpatialFormat              string
	IsTextSubtitleStream            bool
	SupportsExternalStream          bool
	DisplayTitle                    string
}

// DolbyVision is the DOVI configuration record of a video stream.
type DolbyVision struct {
	VersionMajor, VersionMinor int
	Profile, Level             int
	RPUPresent, ELPresent      int
	BLPresent                  int
	BLSignalCompatibilityID    int
}

// Chapter is a named position in the file.
type Chapter struct {
	Start time.Duration
	Name  string
}

type ffStream struct {
	Index              int               `json:"index"`
	CodecType          string            `json:"codec_type"`
	CodecName          string            `json:"codec_name"`
	CodecTagString     string            `json:"codec_tag_string"`
	Profile            string            `json:"profile"`
	TimeBase           string            `json:"time_base"`
	BitRate            string            `json:"bit_rate"`
	Width              int               `json:"width"`
	Height             int               `json:"height"`
	Level              int               `json:"level"`
	PixFmt             string            `json:"pix_fmt"`
	BitsPerRawSample   string            `json:"bits_per_raw_sample"`
	BitsPerSample      int               `json:"bits_per_sample"`
	FieldOrder         string            `json:"field_order"`
	IsAVC              string            `json:"is_avc"`
	NalLengthSize      string            `json:"nal_length_size"`
	SampleAspectRatio  string            `json:"sample_aspect_ratio"`
	DisplayAspectRatio string            `json:"display_aspect_ratio"`
	ColorSpace         string            `json:"color_space"`
	ColorTransfer      string            `json:"color_transfer"`
	ColorPrimaries     string            `json:"color_primaries"`
	AvgFrameRate       string            `json:"avg_frame_rate"`
	RFrameRate         string            `json:"r_frame_rate"`
	Channels           int               `json:"channels"`
	ChannelLayout      string            `json:"channel_layout"`
	SampleRate         string            `json:"sample_rate"`
	Extradata          string            `json:"extradata"`
	Disposition        map[string]int    `json:"disposition"`
	Tags               map[string]string `json:"tags"`
	SideData           []map[string]any  `json:"side_data_list"`
}

type ffProbe struct {
	Streams  []ffStream `json:"streams"`
	Chapters []struct {
		StartTime string            `json:"start_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

// Parse interprets stored ffprobe JSON.
func Parse(probeJSON string, loc Locale) (Info, error) {
	var p ffProbe
	if err := json.Unmarshal([]byte(probeJSON), &p); err != nil {
		return Info{}, fmt.Errorf("parse probe data: %w", err)
	}
	var info Info
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil {
		info.Duration = time.Duration(d * float64(time.Second))
	}
	info.BitRate, _ = strconv.ParseInt(p.Format.BitRate, 10, 64)
	w := loc.words()
	for _, fs := range p.Streams {
		s, ok := convertStream(fs)
		if !ok {
			continue
		}
		s.DisplayTitle = s.displayTitle(loc)
		info.Streams = append(info.Streams, s)
	}
	info.Container = normalizeContainer(p.Format.FormatName, info.Streams)
	for i, c := range p.Chapters {
		start, err := strconv.ParseFloat(c.StartTime, 64)
		if err != nil {
			continue
		}
		name := tag(c.Tags, "title")
		if name == "" {
			name = fmt.Sprintf("%s %d", w.Chapter, i+1)
		}
		info.Chapters = append(info.Chapters, Chapter{Start: time.Duration(start * float64(time.Second)), Name: name})
	}
	return info, nil
}

func tag(tags map[string]string, key string) string {
	if v, ok := tags[key]; ok {
		return v
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func convertStream(f ffStream) (Stream, bool) {
	s := Stream{
		Index:    f.Index,
		Codec:    f.CodecName,
		Profile:  f.Profile,
		Language: tag(f.Tags, "language"),
		Title:    tag(f.Tags, "title"),
		TimeBase: f.TimeBase,

		IsDefault:         f.Disposition["default"] == 1,
		IsForced:          f.Disposition["forced"] == 1,
		IsHearingImpaired: f.Disposition["hearing_impaired"] == 1,
		IsOriginal:        f.Disposition["original"] == 1,
	}
	if f.CodecTagString != "" && !strings.Contains(f.CodecTagString, "[0]") {
		s.CodecTag = f.CodecTagString
	}
	s.BitRate, _ = strconv.ParseInt(f.BitRate, 10, 64)
	if s.BitRate == 0 {
		for _, k := range []string{"BPS", "BPS-eng"} {
			if v, err := strconv.ParseInt(tag(f.Tags, k), 10, 64); err == nil {
				s.BitRate = v
				break
			}
		}
	}
	switch f.CodecType {
	case "video":
		if f.Disposition["attached_pic"] == 1 {
			s.Type = "EmbeddedImage"
			s.Width, s.Height = f.Width, f.Height
			return s, true
		}
		s.Type = "Video"
		fillVideo(&s, f)
	case "audio":
		s.Type = "Audio"
		s.Channels = f.Channels
		if f.ChannelLayout != "" {
			s.ChannelLayout, _, _ = strings.Cut(f.ChannelLayout, "(")
		}
		s.SampleRate, _ = strconv.Atoi(f.SampleRate)
		if f.BitsPerSample > 0 {
			s.BitDepth = f.BitsPerSample
		} else if n, err := strconv.Atoi(f.BitsPerRawSample); err == nil {
			s.BitDepth = n
		}
		switch {
		case strings.Contains(strings.ToLower(s.Profile), "dolby atmos"):
			s.AudioSpatialFormat = "DolbyAtmos"
		case strings.Contains(strings.ToUpper(s.Profile), "DTS:X"):
			s.AudioSpatialFormat = "DTSX"
		default:
			s.AudioSpatialFormat = "None"
		}
	case "subtitle":
		s.Type = "Subtitle"
		s.Codec = normalizeSubtitleCodec(s.Codec)
		s.IsTextSubtitleStream = isTextSubtitle(s.Codec)
		// Embedded subtitles reach the client inside the container. Serving
		// them as separate files would mean reading the whole file with
		// ffmpeg, which homeflix does not offer yet.
		s.SupportsExternalStream = false
	case "data":
		s.Type = "Data"
	default:
		return Stream{}, false
	}
	return s, true
}

func fillVideo(s *Stream, f ffStream) {
	s.Width, s.Height = f.Width, f.Height
	if f.Level > 0 {
		s.Level = float64(f.Level)
	}
	s.PixelFormat = f.PixFmt
	s.ColorSpace, s.ColorTransfer, s.ColorPrimaries = f.ColorSpace, f.ColorTransfer, f.ColorPrimaries
	s.NalLengthSize = f.NalLengthSize
	switch f.IsAVC {
	case "true", "1":
		t := true
		s.IsAVC = &t
	case "false", "0":
		v := false
		s.IsAVC = &v
	}
	s.IsInterlaced = f.FieldOrder != "" && f.FieldOrder != "progressive" && f.FieldOrder != "unknown"
	if n, err := strconv.Atoi(f.BitsPerRawSample); err == nil && n > 0 {
		s.BitDepth = n
	} else {
		switch {
		case strings.Contains(f.PixFmt, "p10"):
			s.BitDepth = 10
		case strings.Contains(f.PixFmt, "p12"):
			s.BitDepth = 12
		case strings.HasPrefix(f.PixFmt, "yuv") || strings.HasPrefix(f.PixFmt, "yuvj") || strings.HasPrefix(f.PixFmt, "nv12"):
			s.BitDepth = 8
		}
	}
	s.AverageFrameRate = parseRate(f.AvgFrameRate)
	s.RealFrameRate = parseRate(f.RFrameRate)
	s.AspectRatio = aspectRatio(f)
	s.IsAnamorphic = anamorphic(f.SampleAspectRatio)
	if s.Codec == "h264" {
		if b, err := ParseHexdump(f.Extradata); err == nil {
			if n, err := H264RefFrames(b); err == nil {
				s.RefFrames = n
			}
		}
	}
	for _, sd := range f.SideData {
		if t, _ := sd["side_data_type"].(string); t == "DOVI configuration record" {
			num := func(k string) int {
				v, _ := sd[k].(float64)
				return int(v)
			}
			s.DV = &DolbyVision{
				VersionMajor: num("dv_version_major"), VersionMinor: num("dv_version_minor"),
				Profile: num("dv_profile"), Level: num("dv_level"),
				RPUPresent: num("rpu_present_flag"), ELPresent: num("el_present_flag"), BLPresent: num("bl_present_flag"),
				BLSignalCompatibilityID: num("dv_bl_signal_compatibility_id"),
			}
		}
	}
	s.VideoRange, s.VideoRangeType = videoRange(*s)
}

func parseRate(r string) float64 {
	num, den, ok := strings.Cut(r, "/")
	if !ok {
		v, _ := strconv.ParseFloat(r, 64)
		return v
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return float64(int(n/d*1000+0.5)) / 1000
}

func aspectRatio(f ffStream) string {
	if f.DisplayAspectRatio != "" && f.DisplayAspectRatio != "0:1" {
		return f.DisplayAspectRatio
	}
	if f.Width <= 0 || f.Height <= 0 {
		return ""
	}
	g := gcd(f.Width, f.Height)
	return fmt.Sprintf("%d:%d", f.Width/g, f.Height/g)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func anamorphic(sar string) bool {
	num, den, ok := strings.Cut(sar, ":")
	if !ok {
		return false
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || n == 0 || d == 0 {
		return false
	}
	r := n / d
	return r < 0.99 || r > 1.01
}

// videoRange follows Jellyfin's rules, so device profile conditions on
// VideoRangeType evaluate the same way. HDR10+ is not detected.
func videoRange(s Stream) (string, string) {
	isPQ := strings.EqualFold(s.ColorTransfer, "smpte2084")
	isHLG := strings.EqualFold(s.ColorTransfer, "arib-std-b67")
	base := "SDR"
	if isPQ || isHLG {
		base = "HDR"
	}
	dv := s.DV
	tagDV := false
	switch strings.ToLower(s.CodecTag) {
	case "dovi", "dvh1", "dvhe", "dav1":
		tagDV = true
	}
	profileDV := dv != nil && (dv.Profile == 5 || dv.Profile == 7 || dv.Profile == 8 || dv.Profile == 10)
	flagDV := dv != nil && dv.RPUPresent == 1 && dv.BLPresent == 1
	if dv != nil && flagDV {
		switch dv.BLSignalCompatibilityID {
		case 0, 1, 2, 4, 6:
		default:
			flagDV = false
		}
	}
	if (profileDV && flagDV) || tagDV {
		var rng, typ string
		profile, compat := 0, -1
		if dv != nil {
			profile, compat = dv.Profile, dv.BLSignalCompatibilityID
		}
		switch profile {
		case 5:
			rng, typ = "HDR", "DOVI"
		case 8:
			switch compat {
			case 1:
				rng, typ = "HDR", "DOVIWithHDR10"
			case 4:
				rng, typ = "HDR", "DOVIWithHLG"
			case 2:
				rng, typ = "SDR", "DOVIWithSDR"
			default:
				rng, typ = base, "DOVIInvalid"
			}
		case 7:
			rng, typ = "HDR", "DOVIWithEL"
		case 10:
			switch compat {
			case 0:
				rng, typ = "HDR", "DOVI"
			case 1:
				rng, typ = "HDR", "DOVIWithHDR10"
			case 2:
				rng, typ = "SDR", "DOVIWithSDR"
			case 4:
				rng, typ = "HDR", "DOVIWithHLG"
			default:
				rng, typ = base, "DOVIInvalid"
			}
		default:
			rng, typ = "SDR", "SDR"
		}
		expected := ""
		switch typ {
		case "DOVIWithHDR10", "DOVIWithEL":
			expected = "smpte2084"
		case "DOVIWithHLG":
			expected = "arib-std-b67"
		}
		if expected != "" && (!strings.EqualFold(s.ColorSpace, "bt2020nc") ||
			!strings.EqualFold(s.ColorTransfer, expected) || !strings.EqualFold(s.ColorPrimaries, "bt2020")) {
			return base, "DOVIInvalid"
		}
		return rng, typ
	}
	switch {
	case isPQ:
		return "HDR", "HDR10"
	case isHLG:
		return "HDR", "HLG"
	}
	return "SDR", "SDR"
}

func normalizeSubtitleCodec(c string) string {
	switch strings.ToLower(c) {
	case "dvb_subtitle":
		return "DVBSUB"
	case "dvb_teletext":
		return "DVBTXT"
	case "dvd_subtitle":
		return "DVDSUB"
	case "hdmv_pgs_subtitle":
		return "PGSSUB"
	}
	return c
}

func isTextSubtitle(c string) bool {
	l := strings.ToLower(c)
	if strings.Contains(l, "microdvd") {
		return true
	}
	return !strings.Contains(l, "pgs") && !strings.Contains(l, "dvdsub") && !strings.Contains(l, "vobsub") &&
		!strings.Contains(l, "dvbsub") && l != "sup" && l != "sub" && l != ""
}

var webmVideo = map[string]bool{"vp8": true, "vp9": true, "av1": true}
var webmAudio = map[string]bool{"vorbis": true, "opus": true}

// normalizeContainer turns ffprobe's format name into Jellyfin's
// container string.
func normalizeContainer(format string, streams []Stream) string {
	if strings.TrimSpace(format) == "" {
		return ""
	}
	webmOK := true
	for _, s := range streams {
		switch s.Type {
		case "Video":
			webmOK = webmOK && webmVideo[strings.ToLower(s.Codec)]
		case "Audio":
			webmOK = webmOK && webmAudio[strings.ToLower(s.Codec)]
		default:
			webmOK = false
		}
	}
	var out []string
	for _, f := range strings.Split(format, ",") {
		switch strings.ToLower(f) {
		case "mpegvideo":
			f = "mpeg"
		case "mpegts":
			f = "ts"
		case "matroska":
			f = "mkv"
		case "webm":
			if !webmOK {
				continue
			}
		}
		if f != "" {
			out = append(out, f)
		}
	}
	return strings.Join(out, ",")
}

func audioFriendlyName(codec string) string {
	switch strings.ToLower(codec) {
	case "ac3":
		return "Dolby Digital"
	case "eac3":
		return "Dolby Digital+"
	case "dca":
		return "DTS"
	}
	return strings.ToUpper(codec)
}

func (s Stream) resolutionText() string {
	if s.Width == 0 || s.Height == 0 {
		return ""
	}
	sfx := "p"
	if s.IsInterlaced {
		sfx = "i"
	}
	w, h := s.Width, s.Height
	switch {
	case w <= 256 && h <= 144:
		return "144" + sfx
	case w <= 426 && h <= 240:
		return "240" + sfx
	case w <= 640 && h <= 360:
		return "360" + sfx
	case w <= 682 && h <= 384:
		return "384" + sfx
	case w <= 720 && h <= 404:
		return "404" + sfx
	case w <= 854 && h <= 480:
		return "480" + sfx
	case w <= 960 && h <= 544:
		return "540" + sfx
	case w <= 1024 && h <= 576:
		return "576" + sfx
	case w <= 1280 && h <= 962:
		return "720" + sfx
	case w <= 2560 && h <= 1440:
		return "1080" + sfx
	case w <= 4096 && h <= 3072:
		return "4K"
	case w <= 8192 && h <= 6144:
		return "8K"
	}
	return ""
}

func (s Stream) doviTitle() string {
	dv := s.DV
	if dv == nil || dv.RPUPresent != 1 || dv.BLPresent != 1 {
		return ""
	}
	switch dv.Profile {
	case 4, 5, 7, 8, 9, 10:
	default:
		return ""
	}
	t := fmt.Sprintf("Dolby Vision Profile %d", dv.Profile)
	if dv.BLSignalCompatibilityID > 0 {
		t += fmt.Sprintf(".%d", dv.BLSignalCompatibilityID)
	}
	switch dv.BLSignalCompatibilityID {
	case 1, 6:
		t += " (HDR10)"
	case 2:
		t += " (SDR)"
	case 4:
		t += " (HLG)"
	}
	return t
}

// withTitle prefixes the stream title and keeps only attributes the title
// does not already mention, as Jellyfin does.
func (s Stream) withTitle(attrs []string, sep string) string {
	if s.Title == "" {
		return strings.Join(attrs, sep)
	}
	var b strings.Builder
	b.WriteString(s.Title)
	lower := strings.ToLower(s.Title)
	for _, a := range attrs {
		if !strings.Contains(lower, strings.ToLower(a)) {
			b.WriteString(" - ")
			b.WriteString(a)
		}
	}
	return b.String()
}

func (s Stream) displayTitle(loc Locale) string {
	w := loc.words()
	var attrs []string
	switch s.Type {
	case "Audio":
		if s.Language != "" && !specialLanguages[strings.ToLower(s.Language)] {
			attrs = append(attrs, firstUpper(loc.LanguageName(s.Language)))
		}
		if s.Profile != "" && !strings.EqualFold(s.Profile, "lc") {
			attrs = append(attrs, s.Profile)
		} else if s.Codec != "" {
			attrs = append(attrs, audioFriendlyName(s.Codec))
		}
		if s.ChannelLayout != "" {
			attrs = append(attrs, firstUpper(s.ChannelLayout))
		} else if s.Channels > 0 {
			attrs = append(attrs, fmt.Sprintf("%d ch", s.Channels))
		}
		if s.IsDefault {
			attrs = append(attrs, w.Default)
		}
		if s.IsOriginal {
			attrs = append(attrs, w.Original)
		}
		return s.withTitle(attrs, " - ")
	case "Video":
		if r := s.resolutionText(); r != "" {
			attrs = append(attrs, r)
		}
		if s.Codec != "" {
			attrs = append(attrs, strings.ToUpper(s.Codec))
		}
		if t := s.doviTitle(); t != "" {
			attrs = append(attrs, t)
		} else if s.VideoRange != "" {
			attrs = append(attrs, s.VideoRange)
		}
		if s.Title == "" {
			return strings.Join(attrs, " ")
		}
		return s.withTitle(attrs, " - ")
	case "Subtitle":
		if s.Language != "" {
			attrs = append(attrs, firstUpper(loc.LanguageName(s.Language)))
		} else {
			attrs = append(attrs, w.Undefined)
		}
		if s.IsHearingImpaired {
			attrs = append(attrs, w.HearingImpaired)
		}
		if s.IsDefault {
			attrs = append(attrs, w.Default)
		}
		if s.IsForced {
			attrs = append(attrs, w.Forced)
		}
		if s.Codec != "" {
			attrs = append(attrs, strings.ToUpper(s.Codec))
		}
		return s.withTitle(attrs, " - ")
	}
	return ""
}

// DefaultAudioIndex returns the stream index of the audio track marked
// default, else the first audio track, else -1.
func (i Info) DefaultAudioIndex() int {
	first := -1
	for _, s := range i.Streams {
		if s.Type != "Audio" {
			continue
		}
		if s.IsDefault {
			return s.Index
		}
		if first < 0 {
			first = s.Index
		}
	}
	return first
}

// DefaultSubtitleIndex returns the subtitle track marked default, else a
// forced track in the language of the default audio, else -1.
func (i Info) DefaultSubtitleIndex() int {
	audioLang := ""
	if a := i.DefaultAudioIndex(); a >= 0 {
		for _, s := range i.Streams {
			if s.Index == a {
				audioLang = strings.ToLower(s.Language)
			}
		}
	}
	forced := -1
	for _, s := range i.Streams {
		if s.Type != "Subtitle" {
			continue
		}
		if s.IsDefault {
			return s.Index
		}
		if forced < 0 && s.IsForced && strings.ToLower(s.Language) == audioLang {
			forced = s.Index
		}
	}
	return forced
}

// Video returns the main video stream, if any.
func (i Info) Video() (Stream, bool) {
	for _, s := range i.Streams {
		if s.Type == "Video" {
			return s, true
		}
	}
	return Stream{}, false
}
