package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeToken has the shape of a TMDB API Read Access Token (a JWT).
const fakeToken = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ0ZXN0In0.c2lnbmF0dXJl"

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTMDBBlock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tmdb.token", fakeToken+"\n", 0o600)
	conf := writeFile(t, dir, "homeflix.conf", "language de\ntmdb {\n\ttoken file \"tmdb.token\"\n}\n", 0o644)
	cfg, err := Load(conf)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TMDB == nil || cfg.TMDB.Token != fakeToken || cfg.TMDB.TokenFile != filepath.Join(dir, "tmdb.token") {
		t.Fatalf("tmdb: %+v", cfg.TMDB)
	}
	if cfg.TMDB.Language != "de-DE" {
		t.Errorf("language %q, want de-DE from 'language de'", cfg.TMDB.Language)
	}

	conf = writeFile(t, dir, "other.conf", "tmdb {\n\ttoken file tmdb.token\n\tlanguage fr-FR\n}\n", 0o644)
	if cfg, err := Load(conf); err != nil || cfg.TMDB.Language != "fr-FR" {
		t.Errorf("explicit language: %+v, %v", cfg.TMDB, err)
	}
	if cfg, err := parseString(t, "no tmdb\n"); err != nil || cfg.TMDB != nil {
		t.Errorf("no tmdb: %+v, %v", cfg.TMDB, err)
	}
	if cfg, _ := parseString(t, ""); cfg.TMDB != nil {
		t.Error("tmdb enabled without a tmdb block")
	}
}

func TestTMDBErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "open.token", fakeToken, 0o644)
	writeFile(t, dir, "v3.token", "0123456789abcdef0123456789abcdef", 0o600)
	writeFile(t, dir, "junk.token", "not a token", 0o600)
	writeFile(t, dir, "empty.token", "\n", 0o600)
	for name, tc := range map[string]struct{ src, want string }{
		"readable by others": {"tmdb { token file \"open.token\" }", "group writable or world read/writable"},
		"v3 key":             {"tmdb { token file \"v3.token\" }", "API key (v3)"},
		"junk":               {"tmdb { token file \"junk.token\" }", "does not hold an API Read Access Token"},
		"empty":              {"tmdb { token file \"empty.token\" }", "is empty"},
		"missing file":       {"tmdb { token file \"none.token\" }", "no such file"},
		"no token":           {"tmdb { language de-DE }", `tmdb block needs "token file"`},
		"bad language":       {"tmdb {\n\ttoken file \"v3.token\"\n\tlanguage german\n}", `language "german"`},
		"unknown statement":  {"tmdb { key abc }", `unknown statement "key" in tmdb block`},
		"twice":              {"no tmdb\nno tmdb", `"tmdb" given twice`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(filepath.Join(dir, "homeflix.conf"), strings.NewReader(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
