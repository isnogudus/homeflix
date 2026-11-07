package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The configuration files and examples shipped with homeflix must parse.

// withTestToken points the token file of examples at a test token.
func withTestToken(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmdb.token")
	if err := os.WriteFile(path, []byte(fakeToken), 0o600); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(src, "/usr/local/etc/homeflix-tmdb.token", path)
}

func TestShippedFilesParse(t *testing.T) {
	for _, name := range []string{"dev.conf", "homeflix.conf.sample"} {
		cfg, err := Load(filepath.Join("..", "..", "deploy", name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(cfg.Warnings) > 0 {
			t.Errorf("%s: warnings %v", name, cfg.Warnings)
		}
	}
}

// commented matches a disabled statement such as "#listen on * port 8096",
// but not prose comments, which have a space after the hash.
var commented = regexp.MustCompile(`(?m)^#([a-z$\t}])`)

func TestSampleStatementsParseWhenEnabled(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "homeflix.conf.sample"))
	if err != nil {
		t.Fatal(err)
	}
	src := withTestToken(t, commented.ReplaceAllString(string(b), "$1"))
	cfg, err := Parse(filepath.Join(t.TempDir(), "homeflix.conf"), strings.NewReader(src))
	if err != nil {
		t.Fatalf("enabled sample:\n%s\n%v", src, err)
	}
	if len(cfg.Libraries) != 3 || len(cfg.Libraries[0].Paths) != 2 || cfg.Libraries[1].Type != Music ||
		cfg.Libraries[2].Type != Audiobooks {
		t.Errorf("sample libraries: %+v", cfg.Libraries)
	}
}

func TestManualExamplesParse(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "homeflix.conf.5"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile(`(?s)\.Bd -literal -offset indent\n(.*?)\.Ed`).FindAllStringSubmatch(string(b), -1)
	if len(blocks) < 3 {
		t.Fatalf("found %d examples in the manual", len(blocks))
	}
	for _, m := range blocks {
		src := withTestToken(t, strings.ReplaceAll(m[1], `\e`, `\`))
		if _, err := parseString(t, src); err != nil {
			t.Errorf("manual example:\n%s\n%v", src, err)
		}
	}
}
