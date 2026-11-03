package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/isnogudus/obsdconf"
)

func parseString(t *testing.T, src string) (Config, error) {
	t.Helper()
	return Parse("/etc/homeflix.conf", strings.NewReader(src))
}

func TestDefaults(t *testing.T) {
	cfg, err := parseString(t, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("empty file:\n got %+v\nwant %+v", cfg, want)
	}
	if cfg.Listen != ":8096" || cfg.ScanInterval != 6*time.Hour || cfg.DisplayLanguage != "en" {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestFullConfiguration(t *testing.T) {
	cfg, err := parseString(t, `
# Example from homeflix.conf(5).
recent = "/media/movies/recent"
movies = "/media/movies"
tools = "/usr/local/bin"

server name "homeflix dev"
listen on 192.168.1.10 port 18096
data directory data
log level debug
language de
ffprobe $tools/ffprobe
ffmpeg "/usr/local/bin/ffmpeg"
probe workers 8
image workers 3
scan interval 1h30m
sort articles { The, der die das }

library "Filme" {
	path $recent
	path { $movies/archive, "/srv/neue filme" }
}
library Doku { path ${movies}_doku }
`)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		ServerName:      "homeflix dev",
		Listen:          "192.168.1.10:18096",
		DataDir:         "/etc/data",
		LogLevel:        "debug",
		FFprobe:         "/usr/local/bin/ffprobe",
		FFmpeg:          "/usr/local/bin/ffmpeg",
		ProbeWorkers:    8,
		ImageWorkers:    3,
		ScanInterval:    90 * time.Minute,
		SortArticles:    []string{"the", "der", "die", "das"},
		DisplayLanguage: "de",
		Discovery:       true,
		Libraries: []Library{
			{Name: "Filme", Paths: []string{"/media/movies/recent", "/media/movies/archive", "/srv/neue filme"}},
			{Name: "Doku", Paths: []string{"/media/movies_doku"}},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("\n got %+v\nwant %+v", cfg, want)
	}
}

func TestListenForms(t *testing.T) {
	for src, want := range map[string]string{
		"listen on *":                ":8096",
		"listen on * port 80":        ":80",
		"listen on 127.0.0.1":        "127.0.0.1:8096",
		"listen on ::1 port 18096":   "[::1]:18096",
		"listen on fd00::5 port 443": "[fd00::5]:443",
	} {
		cfg, err := parseString(t, src)
		if err != nil || cfg.Listen != want {
			t.Errorf("%q: listen %q, %v; want %q", src, cfg.Listen, err, want)
		}
	}
}

func TestNegations(t *testing.T) {
	cfg, err := parseString(t, "no scan interval\nno sort articles\nno discovery\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ScanInterval != 0 || cfg.SortArticles != nil || cfg.Discovery {
		t.Errorf("negations: %+v", cfg)
	}
	if cfg, err := parseString(t, "discovery\n"); err != nil || !cfg.Discovery {
		t.Errorf("discovery: %v %v", cfg.Discovery, err)
	}
	if _, err := parseString(t, "discovery\nno discovery\n"); err == nil || !strings.Contains(err.Error(), `"discovery" given twice`) {
		t.Errorf("discovery and no discovery: %v", err)
	}
}

func TestErrorsAreReportedTogetherWithLines(t *testing.T) {
	_, err := parseString(t, `server name x
listen on localhost
log level loud
probe workers 0
scan interval 10s
colour red
no sandbox
library a { path movies }
library b {
	path /m
	path /m/sub
	folder /x
}
library A { path /n }
library empty {}
server name y
`)
	var list obsdconf.ErrorList
	if !errors.As(err, &list) {
		t.Fatalf("want ErrorList, got %v", err)
	}
	got := err.Error()
	for _, want := range []string{
		`homeflix.conf:2: invalid address "localhost"`,
		`homeflix.conf:3: expected log level debug, info, warn or error, got "loud"`,
		`homeflix.conf:4: number 0 out of range (1-64)`,
		`homeflix.conf:5: duration "10s" out of range (60s-2592000s)`,
		`homeflix.conf:6: unknown statement "colour"`,
		`homeflix.conf:7: unknown statement "no sandbox"`,
		`homeflix.conf:8: library path "movies" must be absolute`,
		`homeflix.conf:11: library path "/m/sub" overlaps "/m" of library "b" at /etc/homeflix.conf:10`,
		`homeflix.conf:12: unknown statement "folder" in library block`,
		`homeflix.conf:14: library "A" defined twice, first at /etc/homeflix.conf:8`,
		`homeflix.conf:15: library "empty" has no path`,
		`homeflix.conf:16: "server name" given twice, first at /etc/homeflix.conf:1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing error %q in:\n%s", want, got)
		}
	}
	// Libraries whose paths were wrong are not also reported as empty.
	if strings.Contains(got, `library "a" has no path`) || strings.Contains(got, `library "b" has no path`) {
		t.Errorf("follow-up error reported:\n%s", got)
	}
}

func TestLoadIncludesAndWarnsAboutUnusedMacros(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "libraries.conf"), []byte("library Filme { path /media/movies }\n"), 0o644)
	main := filepath.Join(dir, "homeflix.conf")
	os.WriteFile(main, []byte("unused = 1\ninclude \"libraries.conf\"\ndata directory \"db\"\n"), 0o644)
	cfg, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Libraries) != 1 || cfg.Libraries[0].Paths[0] != "/media/movies" {
		t.Errorf("included library: %+v", cfg.Libraries)
	}
	if cfg.DataDir != filepath.Join(dir, "db") {
		t.Errorf("data directory %q not resolved against the file's directory", cfg.DataDir)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "macro unused is not used") {
		t.Errorf("warnings: %v", cfg.Warnings)
	}
	if _, err := Load(filepath.Join(dir, "missing.conf")); err == nil {
		t.Error("missing file: want error")
	}
}

func TestSlogLevel(t *testing.T) {
	cfg := Default()
	cfg.LogLevel = "debug"
	if cfg.SlogLevel() != slog.LevelDebug {
		t.Errorf("SlogLevel() = %v", cfg.SlogLevel())
	}
}
