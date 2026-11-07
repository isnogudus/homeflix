package library

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

var audioExtensions = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true, ".m4b": true, ".aac": true, ".ogg": true, ".oga": true,
	".opus": true, ".wma": true, ".wav": true, ".mpc": true, ".ape": true, ".wv": true, ".aif": true,
	".aiff": true, ".alac": true, ".mka": true, ".dsf": true,
}

// IsAudio reports whether name has the extension of an audio file.
func IsAudio(name string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(name))]
}

// sidecarNames are the files read next to music and audiobooks; .lrc
// files are taken whatever their name.
var sidecarNames = map[string]bool{
	"album.nfo": true, "artist.nfo": true, "metadata.json": true,
	"folder.jpg": true, "folder.jpeg": true, "folder.png": true, "cover.jpg": true, "cover.jpeg": true,
	"cover.png": true, "front.jpg": true, "front.png": true, "artist.jpg": true, "artist.png": true,
	"backdrop.jpg": true, "backdrop.png": true, "fanart.jpg": true, "logo.png": true,
}

// IsSidecar reports whether a file of this name is read as a sidecar.
func IsSidecar(name string) bool {
	n := strings.ToLower(name)
	return sidecarNames[n] || strings.HasSuffix(n, ".lrc")
}

// Sidecar is a sidecar file found on disk.
type Sidecar struct {
	Path string
	File
}

type audioWalker struct {
	log      *slog.Logger
	books    bool
	entries  []Entry
	sidecars []Sidecar
}

// WalkMusic lists the audio files under root, each a track, and the
// sidecar files next to them.
func WalkMusic(root string, log *slog.Logger) ([]Entry, []Sidecar, error) {
	return walkAudio(root, log, false)
}

// WalkAudiobooks lists the audiobooks under root and their sidecar files.
// A folder holding audio files is one book, its files played one after
// another, unless they are several .m4b or .m4a files, which are books of
// their own; files directly in root are books too. Subfolders are looked
// at in turn.
func WalkAudiobooks(root string, log *slog.Logger) ([]Entry, []Sidecar, error) {
	return walkAudio(root, log, true)
}

func walkAudio(root string, log *slog.Logger, books bool) ([]Entry, []Sidecar, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", root)
	}
	w := &audioWalker{log: log, books: books}
	if err := w.dir(root, true); err != nil {
		return nil, nil, err
	}
	sort.Slice(w.entries, func(i, j int) bool { return w.entries[i].Path < w.entries[j].Path })
	sort.Slice(w.sidecars, func(i, j int) bool { return w.sidecars[i].Path < w.sidecars[j].Path })
	return w.entries, w.sidecars, nil
}

func (w *audioWalker) dir(path string, root bool) error {
	des, err := os.ReadDir(path)
	if err != nil {
		if root {
			return err
		}
		w.log.Warn("skip unreadable directory", "path", path, "err", err)
		return nil
	}
	var files []File
	var dirs []string
	for _, de := range des {
		name := de.Name()
		if ignoredName(name) {
			continue
		}
		p := filepath.Join(path, name)
		info, err := os.Stat(p)
		if err != nil {
			w.log.Warn("skip unreadable entry", "path", p, "err", err)
			continue
		}
		switch {
		case info.IsDir():
			dirs = append(dirs, p)
		case !info.Mode().IsRegular():
		case IsAudio(name):
			files = append(files, File{Path: p, Size: info.Size(), ModTime: info.ModTime()})
		case IsSidecar(name):
			w.sidecars = append(w.sidecars, Sidecar{Path: p, File: File{Path: p, Size: info.Size(), ModTime: info.ModTime()}})
		}
	}
	sort.Slice(files, func(i, j int) bool { return NaturalLess(filepath.Base(files[i].Path), filepath.Base(files[j].Path)) })
	switch {
	case !w.books:
		for _, f := range files {
			w.entries = append(w.entries, Entry{Path: f.Path, Type: "Audio", Name: Name{Title: baseTitle(f.Path)}, Files: []File{f}})
		}
	case root || len(files) == 1 || separateBooks(files):
		for _, f := range files {
			w.entries = append(w.entries, Entry{Path: f.Path, Type: "AudioBook", Name: Name{Title: baseTitle(f.Path)}, Files: []File{f}})
		}
	case len(files) > 1:
		e := Entry{Path: path, Type: "AudioBook", Name: Name{Title: cleanText(filepath.Base(path))}}
		for i, f := range files {
			f.Part = i + 1
			e.Files = append(e.Files, f)
		}
		w.entries = append(w.entries, e)
	}
	for _, d := range dirs {
		if err := w.dir(d, false); err != nil {
			return err
		}
	}
	return nil
}

// separateBooks reports whether the audio files of a folder are books of
// their own: several files that each hold a whole book with chapters.
func separateBooks(files []File) bool {
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f.Path)) {
		case ".m4b", ".m4a":
		default:
			return false
		}
	}
	return true
}

func baseTitle(path string) string {
	return cleanText(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
}

// NaturalLess orders names with numbers by their value, so "2 x" comes
// before "10 x"; otherwise case-insensitively.
func NaturalLess(a, b string) bool {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si := i
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			sj := j
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na := strings.TrimLeft(string(ra[si:i]), "0")
			nb := strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	if len(ra)-i != len(rb)-j {
		return len(ra)-i < len(rb)-j
	}
	return a < b
}

// maxSidecarSize caps the descriptions read; larger files are ignored.
const maxSidecarSize = 4 << 20

// ReadSidecar returns what a description file holds, as JSON: the fields
// of an .nfo file (element names in lower case mapping to their values) or
// the compacted metadata.json. Pictures and lyrics give "".
func ReadSidecar(path string) (string, error) {
	name := strings.ToLower(filepath.Base(path))
	if !strings.HasSuffix(name, ".nfo") && name != "metadata.json" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSidecarSize+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxSidecarSize {
		return "", fmt.Errorf("larger than %d bytes", maxSidecarSize)
	}
	if name == "metadata.json" {
		var buf bytes.Buffer
		if err := json.Compact(&buf, bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))); err != nil {
			return "", err
		}
		return buf.String(), nil
	}
	fields, err := ParseNFO(b)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(fields)
	return string(out), err
}

// ParseNFO reads the top-level fields of a Kodi or Jellyfin .nfo file.
// Repeated elements (genre, artist) keep all their values; nested
// elements are flattened to their text.
func ParseNFO(b []byte) (map[string][]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	fields := map[string][]string{}
	depth := 0
	var key string
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(fields) > 0 {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				fields["_root"] = []string{strings.ToLower(t.Name.Local)}
			}
			if depth == 2 {
				key = strings.ToLower(t.Name.Local)
				text.Reset()
			}
		case xml.CharData:
			if depth >= 2 {
				text.Write(t)
			}
		case xml.EndElement:
			if depth == 2 && key != "" {
				if v := strings.TrimSpace(text.String()); v != "" {
					fields[key] = append(fields[key], v)
				}
				key = ""
			}
			depth--
		}
	}
	return fields, nil
}
