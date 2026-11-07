package media

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// AudioMeta is what an audio file says about itself: its tags, as tagging
// tools such as MusicBrainz Picard and beets write them, and the facts of
// its audio stream. It is extracted once from the probe data and kept, so
// that the catalog need not parse the probe of every track.
type AudioMeta struct {
	Title           string   `json:"title,omitempty"`
	Artist          string   `json:"artist,omitempty"`
	Artists         []string `json:"artists,omitempty"`
	ArtistSort      string   `json:"artistSort,omitempty"`
	AlbumArtist     string   `json:"albumArtist,omitempty"`
	AlbumArtistSort string   `json:"albumArtistSort,omitempty"`
	Album           string   `json:"album,omitempty"`
	AlbumSort       string   `json:"albumSort,omitempty"`
	Track           int      `json:"track,omitempty"`
	TrackTotal      int      `json:"trackTotal,omitempty"`
	Disc            int      `json:"disc,omitempty"`
	DiscTotal       int      `json:"discTotal,omitempty"`
	Date            string   `json:"date,omitempty"` // as tagged: "1997-10-21", "1997-10" or "1997"
	OriginalYear    int      `json:"originalYear,omitempty"`
	Genres          []string `json:"genres,omitempty"`
	Composer        string   `json:"composer,omitempty"`
	Comment         string   `json:"comment,omitempty"`
	Compilation     bool     `json:"compilation,omitempty"`

	MBAlbumID       string   `json:"mbAlbum,omitempty"`
	MBAlbumArtistID string   `json:"mbAlbumArtist,omitempty"`
	MBArtistIDs     []string `json:"mbArtists,omitempty"`
	MBReleaseGroup  string   `json:"mbReleaseGroup,omitempty"`
	MBTrackID       string   `json:"mbTrack,omitempty"`

	// TrackGain is the ReplayGain track gain in dB (reference -18 LUFS),
	// nil if the file has none.
	TrackGain *float64 `json:"trackGain,omitempty"`
	// Lyrics reports whether the file carries lyrics that are not empty.
	Lyrics bool `json:"lyrics,omitempty"`

	Container     string `json:"container,omitempty"` // as media.Info reports it
	Codec         string `json:"codec,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"layout,omitempty"`
	SampleRate    int    `json:"sampleRate,omitempty"`
	BitDepth      int    `json:"bitDepth,omitempty"`
	BitRate       int64  `json:"bitRate,omitempty"`
	StreamIndex   int    `json:"stream"`
	// Cover is the index of the embedded picture, -1 if there is none.
	Cover int `json:"cover"`
	// CoverCodec is the codec of the embedded picture (mjpeg, png).
	CoverCodec string `json:"coverCodec,omitempty"`
}

// Year is the year of the date tag, 0 if there is none.
func (a AudioMeta) Year() int {
	if len(a.Date) >= 4 {
		if y, err := strconv.Atoi(a.Date[:4]); err == nil {
			return y
		}
	}
	return 0
}

// ReleaseDate is the full date of the date tag, the zero time if it holds
// only a year or nothing.
func (a AudioMeta) ReleaseDate() time.Time {
	for _, layout := range []string{"2006-01-02", "2006-01"} {
		if t, err := time.Parse(layout, a.Date); err == nil {
			return t
		}
	}
	if y := a.Year(); y > 0 {
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Time{}
}

// AllArtists returns the track's artists: the multi-valued artists tag if
// there is one, else the artist tag.
func (a AudioMeta) AllArtists() []string {
	if len(a.Artists) > 0 {
		return a.Artists
	}
	if a.Artist != "" {
		return []string{a.Artist}
	}
	return nil
}

// Info describes the file as Parse would, from the kept facts alone: one
// audio stream and the embedded picture.
func (a AudioMeta) Info(d time.Duration, loc Locale) Info {
	info := Info{Container: a.Container, Duration: d, BitRate: a.BitRate}
	s := Stream{Index: a.StreamIndex, Type: "Audio", Codec: a.Codec, Profile: a.Profile, BitRate: a.BitRate,
		Channels: a.Channels, ChannelLayout: a.ChannelLayout, SampleRate: a.SampleRate, BitDepth: a.BitDepth,
		IsDefault: true, AudioSpatialFormat: "None"}
	s.DisplayTitle = s.displayTitle(loc)
	info.Streams = append(info.Streams, s)
	if a.Cover >= 0 {
		info.Streams = append(info.Streams, Stream{Index: a.Cover, Type: "EmbeddedImage", Codec: a.CoverCodec})
	}
	return info
}

// tagKey folds the spellings of a tag name in ID3, Vorbis comments, MP4
// and ASF into one: lower case without spaces, underscores, dashes and
// slashes, so "MusicBrainz Album Id", "MUSICBRAINZ_ALBUMID" and
// "MusicBrainz/Album Id" are all "musicbrainzalbumid".
func tagKey(k string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(k) {
		switch r {
		case ' ', '_', '-', '/', ':', '.':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type audioProbe struct {
	Streams []ffStream `json:"streams"`
	Format  struct {
		FormatName string            `json:"format_name"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

// ParseAudio extracts the AudioMeta of a probed audio file.
func ParseAudio(probeJSON string) (AudioMeta, error) {
	var p audioProbe
	if err := json.Unmarshal([]byte(probeJSON), &p); err != nil {
		return AudioMeta{}, fmt.Errorf("parse probe data: %w", err)
	}
	a := AudioMeta{StreamIndex: -1, Cover: -1}
	tags := map[string]string{}
	add := func(m map[string]string) {
		for k, v := range m {
			k = tagKey(k)
			if _, ok := tags[k]; !ok && strings.TrimSpace(v) != "" {
				tags[k] = strings.TrimSpace(v)
			}
		}
	}
	add(p.Format.Tags)
	var streams []Stream
	for _, fs := range p.Streams {
		s, ok := convertStream(fs)
		if !ok {
			continue
		}
		streams = append(streams, s)
		switch {
		case s.Type == "Audio" && a.StreamIndex < 0:
			a.StreamIndex, a.Codec, a.Profile = s.Index, s.Codec, s.Profile
			a.Channels, a.ChannelLayout, a.SampleRate, a.BitDepth = s.Channels, s.ChannelLayout, s.SampleRate, s.BitDepth
			a.BitRate = s.BitRate
			// Ogg and Opus keep their comments in the stream.
			add(fs.Tags)
		case s.Type == "EmbeddedImage" && a.Cover < 0:
			a.Cover, a.CoverCodec = s.Index, s.Codec
		}
	}
	if a.StreamIndex < 0 {
		return a, fmt.Errorf("no audio stream")
	}
	if a.BitRate == 0 {
		a.BitRate, _ = strconv.ParseInt(p.Format.BitRate, 10, 64)
	}
	a.Container = normalizeContainer(p.Format.FormatName, streams)

	get := func(keys ...string) string {
		for _, k := range keys {
			if v := tags[k]; v != "" {
				return v
			}
		}
		return ""
	}
	a.Title = get("title")
	a.Artist = get("artist", "author")
	a.Artists = splitValues(get("artists"))
	a.ArtistSort = get("artistsort", "tsop", "sortartist")
	a.AlbumArtist = get("albumartist", "band", "tpe2")
	a.AlbumArtistSort = get("albumartistsort", "tso2", "sortalbumartist")
	a.Album = get("album")
	a.AlbumSort = get("albumsort", "tsoa", "sortalbum")
	a.Track, a.TrackTotal = numberPair(get("track", "tracknumber", "trck"))
	if n, _ := strconv.Atoi(get("totaltracks", "tracktotal")); n > 0 && a.TrackTotal == 0 {
		a.TrackTotal = n
	}
	a.Disc, a.DiscTotal = numberPair(get("disc", "discnumber", "tpos"))
	if n, _ := strconv.Atoi(get("totaldiscs", "disctotal")); n > 0 && a.DiscTotal == 0 {
		a.DiscTotal = n
	}
	a.Date = normalizeDate(get("date", "year", "tdrc", "tyer", "originaldate"))
	if y, err := strconv.Atoi(firstN(get("originalyear", "originaldate", "tdor"), 4)); err == nil {
		a.OriginalYear = y
	}
	a.Genres = splitValues(get("genre"))
	a.Composer = get("composer")
	a.Comment = get("comment", "description", "desc")
	switch strings.ToLower(get("compilation", "tcmp", "cpil")) {
	case "1", "true", "yes":
		a.Compilation = true
	}
	a.MBAlbumID = mbid(get("musicbrainzalbumid", "musicbrainzreleaseid"))
	a.MBAlbumArtistID = mbid(firstValue(get("musicbrainzalbumartistid")))
	for _, id := range splitValues(get("musicbrainzartistid")) {
		if id = mbid(id); id != "" {
			a.MBArtistIDs = append(a.MBArtistIDs, id)
		}
	}
	a.MBReleaseGroup = mbid(get("musicbrainzreleasegroupid"))
	a.MBTrackID = mbid(get("musicbrainzreleasetrackid", "musicbrainztrackid"))
	if g, ok := parseGain(get("replaygaintrackgain")); ok {
		a.TrackGain = &g
	} else if v, err := strconv.Atoi(get("r128trackgain")); err == nil && v != 0 {
		// R128 gains are Q7.8 dB relative to -23 LUFS; ReplayGain's
		// reference is 5 dB louder.
		g := math.Round((float64(v)/256+5)*100) / 100
		a.TrackGain = &g
	}
	for k, v := range tags {
		if (strings.HasPrefix(k, "lyrics") || strings.HasPrefix(k, "unsyncedlyrics") || k == "uslt") &&
			strings.TrimSpace(v) != "" {
			a.Lyrics = true
		}
	}
	return a, nil
}

// splitValues splits a multi-valued tag; ffprobe joins repeated Vorbis
// comments with ";".
func splitValues(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ";") {
		p = strings.TrimSpace(p)
		if p != "" && !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			out = append(out, p)
		}
	}
	return out
}

func firstValue(s string) string {
	v, _, _ := strings.Cut(s, ";")
	return strings.TrimSpace(v)
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// numberPair reads "3", "3/12" or "03 of 12".
func numberPair(s string) (n, total int) {
	a, b, _ := strings.Cut(s, "/")
	n, _ = strconv.Atoi(strings.TrimSpace(a))
	total, _ = strconv.Atoi(strings.TrimSpace(b))
	return n, total
}

// normalizeDate keeps the date part of a date tag ("1997-10-21T00:00:00"
// becomes "1997-10-21"); anything not starting with a year is dropped.
func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return ""
	}
	if _, err := strconv.Atoi(s[:4]); err != nil {
		return ""
	}
	if len(s) > 10 {
		s = s[:10]
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if _, err := time.Parse(layout, s); err == nil {
			return s
		}
	}
	return s[:4]
}

// mbid accepts a MusicBrainz ID (a UUID) and returns it in lower case,
// "" for anything else.
func mbid(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 36 || strings.Count(s, "-") != 4 {
		return ""
	}
	return s
}

// parseGain reads a ReplayGain value such as "-7.77 dB".
func parseGain(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.ToLower(s)), "db"))
	if s == "" {
		return 0, false
	}
	g, err := strconv.ParseFloat(strings.TrimPrefix(s, "+"), 64)
	return g, err == nil
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
