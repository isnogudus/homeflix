package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "homeflix.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `server_name = "test"`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "test" || cfg.Listen != ":8096" || cfg.DataDir != "/var/db/homeflix" || cfg.LogLevel != "info" {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadResolvesRelativeDataDir(t *testing.T) {
	path := writeConfig(t, `data_dir = "data"`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(path), "data")
	if cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := writeConfig(t, "listen = \":8096\"\nlisten_addr = \":1\"\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "listen_addr") {
		t.Fatalf("want unknown-key error naming listen_addr, got %v", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	for name, content := range map[string]string{
		"listen":      `listen = "8096"`,
		"log level":   `log_level = "loud"`,
		"server name": `server_name = " "`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

func TestSlogLevel(t *testing.T) {
	cfg := Default()
	cfg.LogLevel = "debug"
	l, err := cfg.SlogLevel()
	if err != nil || l != slog.LevelDebug {
		t.Errorf("SlogLevel() = %v, %v", l, err)
	}
}
