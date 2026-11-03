package library

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"homeflix/internal/media"
	"homeflix/internal/store"
)

// Stats summarises the probed files of all libraries.
type Stats struct {
	Movies, Extras  int
	Files, Unprobed int
	Failed          []string // "path: error"
	NoYear          []string // titles of movies without a year
	Containers      Counter  // by file extension
	VideoCodecs     Counter  // codec and profile, e.g. "h264 High"
	Resolutions     Counter
	HDR             Counter
	AudioCodecs     Counter // codec and profile, e.g. "dts DTS-HD MA"
	SubtitleCodecs  Counter
	AudioTracks     Counter // tracks per file
	// H.264 at 1900 px width or more, by reference frames (Moonfin 2.x
	// refuses more than 4 there).
	H264RefFrames        Counter
	H264ManyRefFrames    int
	H264UnknownRefFrames int
}

// Counter counts occurrences of labels.
type Counter map[string]int

// Sorted returns the labels by descending count.
func (c Counter) Sorted() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if c[keys[i]] != c[keys[j]] {
			return c[keys[i]] > c[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

type probeStream struct {
	CodecType     string `json:"codec_type"`
	CodecName     string `json:"codec_name"`
	Profile       string `json:"profile"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Extradata     string `json:"extradata"`
	ColorTransfer string `json:"color_transfer"`
	SideData      []struct {
		Type string `json:"side_data_type"`
	} `json:"side_data_list"`
	Disposition struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
}

func resolution(w, h int) string {
	switch {
	case w >= 3200 || h >= 2000:
		return "2160p"
	case w >= 1800 || h >= 1000:
		return "1080p"
	case w >= 1200 || h >= 700:
		return "720p"
	default:
		return "SD"
	}
}

func hdrKind(s probeStream) string {
	for _, sd := range s.SideData {
		if strings.Contains(sd.Type, "DOVI") {
			return "Dolby Vision"
		}
	}
	switch s.ColorTransfer {
	case "smpte2084":
		return "HDR10"
	case "arib-std-b67":
		return "HLG"
	}
	return "SDR"
}

func label(codec, profile string) string {
	if profile == "" || profile == "unknown" {
		return codec
	}
	return codec + " " + profile
}

// CollectStats reads every file of present items.
func CollectStats(ctx context.Context, st *store.Store) (Stats, error) {
	s := Stats{
		Containers: Counter{}, VideoCodecs: Counter{}, Resolutions: Counter{}, HDR: Counter{},
		AudioCodecs: Counter{}, SubtitleCodecs: Counter{}, AudioTracks: Counter{}, H264RefFrames: Counter{},
	}
	seenItems := map[string]bool{}
	err := st.AllFiles(ctx, func(f store.ProbedFile) error {
		if !seenItems[f.ItemID] {
			seenItems[f.ItemID] = true
			if f.ItemType == "Movie" {
				s.Movies++
				if f.Year == 0 {
					s.NoYear = append(s.NoYear, f.Title)
				}
			} else {
				s.Extras++
			}
		}
		s.Files++
		s.Containers[strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), ".")]++
		switch {
		case f.ProbeError != "":
			s.Failed = append(s.Failed, f.Path+": "+f.ProbeError)
			return nil
		case f.Probe == "":
			s.Unprobed++
			return nil
		}
		var p struct {
			Streams []probeStream `json:"streams"`
		}
		if err := json.Unmarshal([]byte(f.Probe), &p); err != nil {
			s.Failed = append(s.Failed, fmt.Sprintf("%s: stored probe unreadable: %v", f.Path, err))
			return nil
		}
		videoSeen, audio := false, 0
		for _, ps := range p.Streams {
			switch ps.CodecType {
			case "video":
				if ps.Disposition.AttachedPic == 1 || videoSeen {
					continue
				}
				videoSeen = true
				s.VideoCodecs[label(ps.CodecName, ps.Profile)]++
				s.Resolutions[resolution(ps.Width, ps.Height)]++
				s.HDR[hdrKind(ps)]++
				if ps.CodecName == "h264" && ps.Width >= 1900 {
					refs := -1
					if b, err := media.ParseHexdump(ps.Extradata); err == nil {
						if n, err := media.H264RefFrames(b); err == nil {
							refs = n
						}
					}
					switch {
					case refs < 0:
						s.H264UnknownRefFrames++
						s.H264RefFrames["unknown"]++
					case refs > 4:
						s.H264ManyRefFrames++
						fallthrough
					default:
						s.H264RefFrames[fmt.Sprintf("%d", refs)]++
					}
				}
			case "audio":
				audio++
				s.AudioCodecs[label(ps.CodecName, ps.Profile)]++
			case "subtitle":
				s.SubtitleCodecs[ps.CodecName]++
			}
		}
		s.AudioTracks[fmt.Sprintf("%d", audio)]++
		return nil
	})
	sort.Strings(s.NoYear)
	return s, err
}
