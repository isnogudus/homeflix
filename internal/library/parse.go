// Package library finds movies in directories and keeps the database in
// step with them.
package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// videoExtensions are the file types treated as video, lowercased.
var videoExtensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".avi": true, ".mov": true, ".webm": true,
	".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true, ".wmv": true, ".flv": true,
	".ogv": true, ".3gp": true,
}

// IsVideo reports whether a file name has a video extension.
func IsVideo(name string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(name))]
}

// Name is what a file or folder name says about a movie.
type Name struct {
	Title   string
	Year    int    // 0 if absent
	IMDbID  string // "tt…" or ""
	TMDbID  string // digits or ""
	Part    int    // 1, 2, … for split files; 0 otherwise
	Version string // label such as "HD" or "new voices, HD"; "" if none
}

var (
	imdbTag   = regexp.MustCompile(`(?i)[\[{]\s*imdb(?:id)?\s*[-=:]\s*(tt\d{6,10})\s*[\]}]|\[(tt\d{6,10})\]`)
	tmdbTag   = regexp.MustCompile(`(?i)[\[{]\s*tmdb(?:id)?\s*[-=:]\s*(\d+)\s*[\]}]`)
	partTag   = regexp.MustCompile(`(?i)[\s._-]*\b(?:part|pt|cd|dis[ck])[\s._-]*(\d{1,2})$`)
	yearTag   = regexp.MustCompile(`\((\d{4})\)`)
	bracketed = regexp.MustCompile(`\[([^\]]*)\]`)
	spaces    = regexp.MustCompile(`\s+`)
)

const trimSet = " -._"

// ParseName reads a movie file name (extension included) or folder name
// following the scheme "Title (Year) [imdbid-tt…] label part1".
func ParseName(name string, isDir bool) Name {
	s := name
	if !isDir && IsVideo(s) {
		s = strings.TrimSuffix(s, filepath.Ext(s))
	}
	stem := s
	var n Name
	if m := imdbTag.FindStringSubmatch(s); m != nil {
		n.IMDbID = strings.ToLower(m[1] + m[2])
	}
	s = imdbTag.ReplaceAllString(s, " ")
	if m := tmdbTag.FindStringSubmatch(s); m != nil {
		n.TMDbID = m[1]
	}
	s = tmdbTag.ReplaceAllString(s, " ")
	s = strings.TrimRight(s, trimSet)
	if m := partTag.FindStringSubmatchIndex(s); m != nil {
		n.Part, _ = strconv.Atoi(s[m[2]:m[3]])
		s = s[:m[0]]
	}

	rest := ""
	if locs := yearTag.FindAllStringSubmatchIndex(s, -1); len(locs) > 0 {
		loc := locs[len(locs)-1]
		if y, _ := strconv.Atoi(s[loc[2]:loc[3]]); y >= 1880 && y <= 2100 {
			n.Year = y
			rest = s[loc[1]:]
			s = s[:loc[0]]
		}
	}
	n.Title = cleanText(s)
	if n.Title == "" {
		n.Title = cleanText(stem)
	}
	n.Version = versionLabel(rest)
	return n
}

// versionLabel turns what follows the year, such as "[new voices] [HD]",
// into "new voices, HD".
func versionLabel(rest string) string {
	var parts []string
	for _, m := range bracketed.FindAllStringSubmatch(rest, -1) {
		if t := cleanText(m[1]); t != "" {
			parts = append(parts, t)
		}
	}
	if outside := cleanText(bracketed.ReplaceAllString(rest, " ")); outside != "" {
		parts = append([]string{outside}, parts...)
	}
	return strings.Join(parts, ", ")
}

func cleanText(s string) string {
	return strings.Trim(spaces.ReplaceAllString(s, " "), trimSet)
}

// SortKey returns the key movies are ordered by: lowercased, accents
// removed (so "Ärger" sorts with "Arger"), a leading article dropped,
// punctuation ignored and digit runs padded so "2 Guns" comes before
// "10 Minutes Gone".
func SortKey(title string, articles []string) string {
	s := strings.ToLower(norm.NFKD.String(title))
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r == 'ß':
			b.WriteString("ss")
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) > 1 {
		for _, a := range articles {
			if words[0] == strings.ToLower(a) {
				words = words[1:]
				break
			}
		}
	}
	for i, w := range words {
		words[i] = padDigits(w)
	}
	return strings.Join(words, " ")
}

// padDigits left-pads every run of ASCII digits in w to eight digits.
func padDigits(w string) string {
	var b strings.Builder
	for i := 0; i < len(w); {
		j := i
		for j < len(w) && w[j] >= '0' && w[j] <= '9' {
			j++
		}
		if j > i {
			if run := j - i; run < 8 {
				b.WriteString(strings.Repeat("0", 8-run))
			}
			b.WriteString(w[i:j])
			i = j
			continue
		}
		b.WriteByte(w[i])
		i++
	}
	return b.String()
}
