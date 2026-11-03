// Package config reads homeflix.conf(5), a configuration in the style of
// OpenBSD daemons, with github.com/isnogudus/obsdconf.
package config

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/isnogudus/obsdconf"
)

// Config is the complete server configuration.
type Config struct {
	// ServerName is shown to clients. Defaults to the host name.
	ServerName string
	// Listen is the TCP address of the HTTP API, as host:port.
	Listen string
	// DataDir holds the database and caches. A relative path is resolved
	// against the directory of the configuration file.
	DataDir string
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// FFprobe and FFmpeg are looked up in PATH unless absolute.
	FFprobe string
	FFmpeg  string
	// ProbeWorkers and ImageWorkers are the numbers of ffprobe and ffmpeg
	// processes run in parallel.
	ProbeWorkers int
	ImageWorkers int
	// ScanInterval is the time between automatic library scans; 0 turns
	// them off. A scan also runs at startup.
	ScanInterval time.Duration
	// SortArticles are leading words ignored when sorting by name.
	SortArticles []string
	// DisplayLanguage is the language of stream titles: "en" or "de".
	DisplayLanguage string

	// TMDB enables metadata from The Movie Database; nil keeps homeflix
	// offline.
	TMDB *TMDB

	Libraries []Library

	// Warnings are harmless findings, such as unused macros.
	Warnings []string
}

// TMDB configures metadata lookups at The Movie Database.
type TMDB struct {
	// TokenFile holds the API Read Access Token; it must not be readable by
	// others.
	TokenFile string
	// Token is the content of TokenFile.
	Token string
	// Language of titles and overviews, e.g. "de-DE"; English fills gaps.
	Language string
}

// Library is one movie library made of one or more directories.
type Library struct {
	Name  string
	Paths []string
}

// DefaultPort is the Jellyfin port clients try first.
const DefaultPort = 8096

// Default returns the configuration used for statements the file does not
// contain.
func Default() Config {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "homeflix"
	}
	return Config{
		ServerName:      name,
		Listen:          net.JoinHostPort("", strconv.Itoa(DefaultPort)),
		DataDir:         "/var/db/homeflix",
		LogLevel:        "info",
		FFprobe:         "ffprobe",
		FFmpeg:          "ffmpeg",
		ProbeWorkers:    4,
		ImageWorkers:    2,
		ScanInterval:    6 * time.Hour,
		SortArticles:    []string{"the", "a", "an"},
		DisplayLanguage: "en",
	}
}

var keywords = []string{
	"articles", "data", "directory", "ffmpeg", "ffprobe", "file", "image", "interval",
	"language", "level", "library", "listen", "log", "name", "no", "on", "path",
	"port", "probe", "scan", "server", "sort", "tmdb", "token", "workers",
}

// Load reads the configuration file at path; "-" reads standard input. On
// failure the error is an obsdconf.ErrorList with every problem found.
func Load(path string) (Config, error) {
	op, err := obsdconf.NewFile(path, obsdconf.Options{Keywords: keywords})
	if err != nil {
		return Config{}, err
	}
	return parse(op, path)
}

// Parse reads a configuration from r. name is used in error messages and as
// the base for relative paths and includes.
func Parse(name string, r io.Reader) (Config, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return Config{}, err
	}
	return parse(obsdconf.New(name, src, obsdconf.Options{Keywords: keywords}), name)
}

type parser struct {
	*obsdconf.Parser
	cfg  Config
	base string // directory relative paths are resolved against
	seen map[string]obsdconf.Pos
	// libraryPaths maps each library path to where it was given.
	libraryPaths map[string]libraryPath
}

type libraryPath struct {
	library string
	pos     obsdconf.Pos
}

func parse(op *obsdconf.Parser, name string) (Config, error) {
	base := "."
	if name != "-" {
		if abs, err := filepath.Abs(name); err == nil {
			base = filepath.Dir(abs)
		}
	}
	p := &parser{Parser: op, cfg: Default(), base: base, seen: map[string]obsdconf.Pos{},
		libraryPaths: map[string]libraryPath{}}
	if err := p.Parse(p.statement); err != nil {
		return Config{}, err
	}
	for _, m := range p.UnusedMacros() {
		p.cfg.Warnings = append(p.cfg.Warnings, fmt.Sprintf("%s: macro %s is not used", m.Pos, m.Name))
	}
	if t := p.cfg.TMDB; t != nil && t.Language == "" {
		t.Language = "en-US"
		if p.cfg.DisplayLanguage == "de" {
			t.Language = "de-DE"
		}
	}
	return p.cfg, nil
}

var tmdbLanguage = regexp.MustCompile(`^[a-z]{2}-[A-Z]{2}$`)

// tmdb parses the tmdb block: token file PATH and language xx-YY.
func (p *parser) tmdb() bool {
	kw := p.Tok()
	if !p.once("tmdb", kw.Pos) {
		return false
	}
	p.Next()
	t := &TMDB{}
	errsBefore := len(p.Errors())
	ok := p.Block("tmdb", func() bool {
		pos := p.Tok().Pos
		switch {
		case p.Accept("token", "file"):
			if !p.once("token file", pos) {
				return false
			}
			path, ok := p.Text("token file")
			if !ok {
				return false
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(p.base, path)
			}
			token, err := readToken(path)
			if err != nil {
				p.Errorf(pos, "token file: %v", err)
				return false
			}
			t.TokenFile, t.Token = path, token
			return true
		case p.Accept("language"):
			if !p.once("tmdb language", pos) {
				return false
			}
			lt := p.Tok()
			v, ok := p.Text("language such as de-DE")
			if ok && !tmdbLanguage.MatchString(v) {
				p.Errorf(lt.Pos, "language %q: want a language and country such as de-DE", v)
				return false
			}
			t.Language = v
			return ok
		}
		p.Errorf(p.Tok().Pos, "unknown statement %q in tmdb block", p.Tok().Text)
		return false
	})
	if !ok || len(p.Errors()) > errsBefore {
		return false
	}
	if t.TokenFile == "" {
		p.Errorf(kw.Pos, `tmdb block needs "token file"`)
		return false
	}
	p.cfg.TMDB = t
	return true
}

var (
	hexKey = regexp.MustCompile(`^[0-9a-f]{32}$`)
	jwt    = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
)

// readToken reads a TMDB API Read Access Token from a file that must be
// private, by the rules of check_file_secrecy() in OpenBSD daemons.
func readToken(path string) (string, error) {
	if err := checkSecrecy(path); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	switch {
	case token == "":
		return "", fmt.Errorf("%s is empty", path)
	case hexKey.MatchString(token):
		return "", fmt.Errorf("%s holds the API key (v3); use the API Read Access Token (v4)", path)
	case !jwt.MatchString(token):
		return "", fmt.Errorf("%s does not hold an API Read Access Token", path)
	}
	return token, nil
}

// once reports an error if the setting named key was given before.
func (p *parser) once(key string, pos obsdconf.Pos) bool {
	if first, ok := p.seen[key]; ok {
		p.Errorf(pos, "%q given twice, first at %s", key, first)
		return false
	}
	p.seen[key] = pos
	return true
}

func (p *parser) statement() bool {
	t := p.Tok()
	switch {
	case p.Accept("server", "name"):
		return p.once("server name", t.Pos) && p.text(&p.cfg.ServerName, "server name")
	case p.Accept("listen", "on"):
		return p.once("listen on", t.Pos) && p.listen()
	case p.Accept("data", "directory"):
		if !p.once("data directory", t.Pos) || !p.text(&p.cfg.DataDir, "data directory") {
			return false
		}
		if !filepath.IsAbs(p.cfg.DataDir) {
			p.cfg.DataDir = filepath.Join(p.base, p.cfg.DataDir)
		}
		return true
	case p.Accept("log", "level"):
		if !p.once("log level", t.Pos) {
			return false
		}
		v, ok := p.Enum("log level debug, info, warn or error", "debug", "info", "warn", "error")
		p.cfg.LogLevel = v
		return ok
	case p.Accept("language"):
		if !p.once("language", t.Pos) {
			return false
		}
		v, ok := p.Enum("language en or de", "en", "de")
		p.cfg.DisplayLanguage = v
		return ok
	case p.Accept("ffprobe"):
		return p.once("ffprobe", t.Pos) && p.text(&p.cfg.FFprobe, "ffprobe path")
	case p.Accept("ffmpeg"):
		return p.once("ffmpeg", t.Pos) && p.text(&p.cfg.FFmpeg, "ffmpeg path")
	case p.Accept("probe", "workers"):
		return p.once("probe workers", t.Pos) && p.number(&p.cfg.ProbeWorkers, 1, 64)
	case p.Accept("image", "workers"):
		return p.once("image workers", t.Pos) && p.number(&p.cfg.ImageWorkers, 1, 16)
	case p.Accept("scan", "interval"):
		if !p.once("scan interval", t.Pos) {
			return false
		}
		d, ok := p.Duration(time.Minute, 30*24*time.Hour)
		p.cfg.ScanInterval = d
		return ok
	case p.Accept("no", "scan", "interval"):
		p.cfg.ScanInterval = 0
		return p.once("scan interval", t.Pos)
	case p.Accept("sort", "articles"):
		if !p.once("sort articles", t.Pos) {
			return false
		}
		p.cfg.SortArticles = nil
		return p.List(func() bool {
			w, ok := p.Text("article")
			p.cfg.SortArticles = append(p.cfg.SortArticles, strings.ToLower(w))
			return ok
		})
	case p.Accept("no", "sort", "articles"):
		p.cfg.SortArticles = nil
		return p.once("sort articles", t.Pos)
	case p.Is("library"):
		return p.library()
	case p.Is("tmdb"):
		return p.tmdb()
	case p.Accept("no", "tmdb"):
		return p.once("tmdb", t.Pos)
	case p.Is("no") && p.Peek(1).Kind == obsdconf.Word:
		p.Errorf(t.Pos, "unknown statement %q", "no "+p.Peek(1).Text)
		return false
	}
	p.Errorf(t.Pos, "unknown statement %q", t.Text)
	return false
}

func (p *parser) text(dst *string, what string) bool {
	v, ok := p.Text(what)
	if ok {
		*dst = v
	}
	return ok
}

func (p *parser) number(dst *int, min, max uint64) bool {
	n, ok := p.Number(min, max)
	if ok {
		*dst = int(n)
	}
	return ok
}

// listen parses the address and optional port after "listen on": "*" for
// all addresses, or an IPv4 or IPv6 address.
func (p *parser) listen() bool {
	host := ""
	if p.Tok().Kind == obsdconf.Word && p.Tok().Text == "*" {
		p.Next()
	} else {
		a, ok := p.Addr()
		if !ok {
			return false
		}
		host = a.String()
	}
	port := uint16(DefaultPort)
	if p.Accept("port") {
		var ok bool
		if port, ok = p.Port(); !ok {
			return false
		}
	}
	p.cfg.Listen = net.JoinHostPort(host, strconv.Itoa(int(port)))
	return true
}

// library parses library NAME { path PATH ... }.
func (p *parser) library() bool {
	kw := p.Tok()
	p.Next()
	name, ok := p.Text("library name")
	if !ok {
		return false
	}
	key := "library " + strings.ToLower(name)
	if first, dup := p.seen[key]; dup {
		p.Errorf(kw.Pos, "library %q defined twice, first at %s", name, first)
		return false
	}
	p.seen[key] = kw.Pos
	lib := Library{Name: name}
	errsBefore := len(p.Errors())
	ok = p.Block("library", func() bool {
		if !p.Accept("path") {
			p.Errorf(p.Tok().Pos, "unknown statement %q in library block", p.Tok().Text)
			return false
		}
		return p.List(func() bool {
			pos := p.Tok().Pos
			path, ok := p.Text("library path")
			if !ok {
				return false
			}
			if !filepath.IsAbs(path) {
				p.Errorf(pos, "library path %q must be absolute", path)
				return false
			}
			path = filepath.Clean(path)
			for other, owner := range p.libraryPaths {
				if path == other || strings.HasPrefix(path, other+"/") || strings.HasPrefix(other, path+"/") {
					p.Errorf(pos, "library path %q overlaps %q of library %q at %s", path, other, owner.library, owner.pos)
					return false
				}
			}
			p.libraryPaths[path] = libraryPath{library: name, pos: pos}
			lib.Paths = append(lib.Paths, path)
			return true
		})
	})
	if !ok || len(p.Errors()) > errsBefore {
		return false
	}
	if len(lib.Paths) == 0 {
		p.Errorf(kw.Pos, "library %q has no path", name)
		return false
	}
	p.cfg.Libraries = append(p.cfg.Libraries, lib)
	return true
}

// SlogLevel converts LogLevel to a slog.Level.
func (c Config) SlogLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return slog.LevelInfo
	}
	return l
}
