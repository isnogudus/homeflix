package library

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File is one video file found on disk.
type File struct {
	Path    string
	Size    int64
	ModTime time.Time
	Version string
	Part    int
}

// Extra is a bonus video belonging to a movie folder.
type Extra struct {
	Type  string // Jellyfin extra type, e.g. "BehindTheScenes"
	Title string
	File  File
}

// Entry is one movie found on disk: a single file, a set of part files, or
// a folder holding versions, parts and extras.
type Entry struct {
	Path   string // the folder, or the (first) file for movies without one
	Name   Name   // title, year and IDs; Part and Version are unused
	Files  []File // sorted by version, then part
	Extras []Extra
}

// extraFolders maps folder names (lowercased) inside a movie folder to the
// Jellyfin extra type of the videos in them.
var extraFolders = map[string]string{
	"behind the scenes": "BehindTheScenes",
	"deleted scenes":    "DeletedScene",
	"featurettes":       "Featurette",
	"interviews":        "Interview",
	"scenes":            "Scene",
	"shorts":            "Short",
	"trailers":          "Trailer",
	"clips":             "Clip",
	"extras":            "Unknown",
	"other":             "Unknown",
}

func ignoredName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "@") ||
		strings.EqualFold(name, "#recycle") || name == "lost+found"
}

type walker struct {
	log     *slog.Logger
	entries []Entry
}

// Walk lists the movies under root. Unreadable subdirectories are logged
// and skipped; an unreadable root is an error.
func Walk(root string, log *slog.Logger) ([]Entry, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	w := &walker{log: log}
	files, dirs, err := w.read(root)
	if err != nil {
		return nil, err
	}
	w.addLooseFiles(files)
	for _, d := range dirs {
		w.dir(d)
	}
	sort.Slice(w.entries, func(i, j int) bool { return w.entries[i].Path < w.entries[j].Path })
	return w.entries, nil
}

// read returns the video files and subdirectories of dir.
func (w *walker) read(dir string) ([]File, []string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var files []File
	var dirs []string
	for _, de := range des {
		name := de.Name()
		if ignoredName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		var info fs.FileInfo
		if de.Type()&fs.ModeSymlink != 0 {
			info, err = os.Stat(path)
		} else {
			info, err = de.Info()
		}
		if err != nil {
			w.log.Warn("skip unreadable entry", "path", path, "err", err)
			continue
		}
		switch {
		case info.IsDir():
			dirs = append(dirs, path)
		case info.Mode().IsRegular() && IsVideo(name):
			files = append(files, File{Path: path, Size: info.Size(), ModTime: info.ModTime()})
		}
	}
	return files, dirs, nil
}

// addLooseFiles turns files outside a movie folder into entries. Part files
// with the same title and year are stacked into one entry.
func (w *walker) addLooseFiles(files []File) {
	stacks := map[string]*Entry{}
	for _, f := range files {
		n := ParseName(filepath.Base(f.Path), false)
		if n.Part == 0 {
			w.entries = append(w.entries, Entry{Path: f.Path, Name: movieName(n), Files: []File{f}})
			continue
		}
		key := fmt.Sprintf("%s\x00%d\x00%s", strings.ToLower(n.Title), n.Year, strings.ToLower(n.Version))
		f.Part = n.Part
		if e, ok := stacks[key]; ok {
			e.Files = append(e.Files, f)
			continue
		}
		stacks[key] = &Entry{Name: movieName(n), Files: []File{f}}
	}
	for _, e := range stacks {
		sortFiles(e.Files)
		e.Path = e.Files[0].Path
		w.entries = append(w.entries, *e)
	}
}

// dir handles a subdirectory: a movie folder if it holds videos itself,
// otherwise a container whose subdirectories are looked at in turn.
func (w *walker) dir(path string) {
	files, dirs, err := w.read(path)
	if err != nil {
		w.log.Warn("skip unreadable directory", "path", path, "err", err)
		return
	}
	if len(files) == 0 {
		for _, d := range dirs {
			w.dir(d)
		}
		return
	}
	e := Entry{Path: path, Name: movieName(ParseName(filepath.Base(path), true))}
	e.Files = versions(files)
	for _, f := range files {
		n := ParseName(filepath.Base(f.Path), false)
		if e.Name.Year == 0 && n.Year != 0 {
			e.Name.Year = n.Year
		}
		if e.Name.IMDbID == "" {
			e.Name.IMDbID = n.IMDbID
		}
		if e.Name.TMDbID == "" {
			e.Name.TMDbID = n.TMDbID
		}
	}
	for _, d := range dirs {
		typ, ok := extraFolders[strings.ToLower(filepath.Base(d))]
		if !ok {
			w.log.Debug("ignore subdirectory of movie folder", "path", d)
			continue
		}
		extras, _, err := w.read(d)
		if err != nil {
			w.log.Warn("skip unreadable directory", "path", d, "err", err)
			continue
		}
		for _, f := range extras {
			title := cleanText(strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path)))
			e.Extras = append(e.Extras, Extra{Type: typ, Title: title, File: f})
		}
	}
	sort.Slice(e.Extras, func(i, j int) bool { return e.Extras[i].File.Path < e.Extras[j].File.Path })
	w.entries = append(w.entries, e)
}

// versions assigns version labels and part numbers to the videos of one
// movie folder. Files sharing a label form one version; a label used by
// several unsplit files is replaced by each file's name, extension
// included, so "Twins.mkv" and "Twins.mp4" stay apart.
func versions(files []File) []File {
	out := make([]File, len(files))
	unsplit := map[string]int{}
	for i, f := range files {
		n := ParseName(filepath.Base(f.Path), false)
		f.Version, f.Part = n.Version, n.Part
		out[i] = f
		if f.Part == 0 {
			unsplit[f.Version]++
		}
	}
	for i, f := range out {
		if f.Part == 0 && unsplit[f.Version] > 1 {
			out[i].Version = filepath.Base(f.Path)
		}
	}
	labels := map[string]bool{}
	for _, f := range out {
		labels[f.Version] = true
	}
	if len(labels) == 1 {
		for i := range out {
			out[i].Version = ""
		}
	}
	sortFiles(out)
	return out
}

func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Version != files[j].Version {
			return files[i].Version < files[j].Version
		}
		if files[i].Part != files[j].Part {
			return files[i].Part < files[j].Part
		}
		return files[i].Path < files[j].Path
	})
}

// movieName keeps the parts of a parsed name that describe the movie.
func movieName(n Name) Name {
	n.Part, n.Version = 0, ""
	return n
}
