package media

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LyricLine is one line of lyrics; Start is nil for lyrics without times.
type LyricLine struct {
	Text  string
	Start *time.Duration
}

// Lyrics are the lyrics of a track with what the LRC header says.
type Lyrics struct {
	Artist, Title, Album string
	Length               time.Duration
	Lines                []LyricLine
}

var (
	lrcTime = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)
	lrcTag  = regexp.MustCompile(`^\[([a-zA-Z#]+):(.*)\]$`)
)

// ParseLyrics reads lyrics in the LRC format, with times, or as plain text.
func ParseLyrics(text string) Lyrics {
	var l Lyrics
	offset := time.Duration(0)
	timed := false
	text = strings.TrimPrefix(text, "\ufeff")
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if m := lrcTag.FindStringSubmatch(line); m != nil && !lrcTime.MatchString(line) {
			v := strings.TrimSpace(m[2])
			switch strings.ToLower(m[1]) {
			case "ar":
				l.Artist = v
			case "ti":
				l.Title = v
			case "al":
				l.Album = v
			case "length":
				if mm, ss, ok := strings.Cut(v, ":"); ok {
					m, _ := strconv.Atoi(mm)
					s, _ := strconv.ParseFloat(ss, 64)
					l.Length = time.Duration(m)*time.Minute + time.Duration(s*float64(time.Second))
				}
			case "offset":
				if ms, err := strconv.Atoi(v); err == nil {
					// A positive offset shows the lines earlier.
					offset = -time.Duration(ms) * time.Millisecond
				}
			}
			continue
		}
		times := lrcTime.FindAllStringSubmatch(line, -1)
		if len(times) == 0 {
			if line != "" || len(l.Lines) > 0 {
				l.Lines = append(l.Lines, LyricLine{Text: line})
			}
			continue
		}
		timed = true
		words := strings.TrimSpace(lrcTime.ReplaceAllString(line, ""))
		for _, t := range times {
			m, _ := strconv.Atoi(t[1])
			s, _ := strconv.Atoi(t[2])
			frac := time.Duration(0)
			if t[3] != "" {
				f, _ := strconv.Atoi(t[3])
				switch len(t[3]) {
				case 1:
					frac = time.Duration(f) * 100 * time.Millisecond
				case 2:
					frac = time.Duration(f) * 10 * time.Millisecond
				default:
					frac = time.Duration(f) * time.Millisecond
				}
			}
			at := max(time.Duration(m)*time.Minute+time.Duration(s)*time.Second+frac+offset, 0)
			l.Lines = append(l.Lines, LyricLine{Text: words, Start: &at})
		}
	}
	if timed {
		// Untimed lines between timed ones are dropped; the rest is sorted.
		var keep []LyricLine
		for _, ln := range l.Lines {
			if ln.Start != nil {
				keep = append(keep, ln)
			}
		}
		sort.SliceStable(keep, func(i, j int) bool { return *keep[i].Start < *keep[j].Start })
		l.Lines = keep
	}
	for len(l.Lines) > 0 && l.Lines[len(l.Lines)-1].Text == "" && l.Lines[len(l.Lines)-1].Start == nil {
		l.Lines = l.Lines[:len(l.Lines)-1]
	}
	return l
}

// EmbeddedLyrics returns the lyrics tag of probed audio, "" if none.
func EmbeddedLyrics(probeJSON string) string {
	var p audioProbe
	if err := jsonUnmarshal(probeJSON, &p); err != nil {
		return ""
	}
	check := func(tags map[string]string) string {
		for k, v := range tags {
			k = tagKey(k)
			if (strings.HasPrefix(k, "lyrics") || strings.HasPrefix(k, "unsyncedlyrics") || k == "uslt") && strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	if v := check(p.Format.Tags); v != "" {
		return v
	}
	for _, s := range p.Streams {
		if v := check(s.Tags); v != "" {
			return v
		}
	}
	return ""
}
