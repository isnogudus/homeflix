// Package config loads and validates the homeflix configuration file.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the complete server configuration.
type Config struct {
	// ServerName is shown to clients. Defaults to the host name.
	ServerName string `toml:"server_name"`
	// Listen is the TCP address of the HTTP API.
	Listen string `toml:"listen"`
	// DataDir holds the database and caches. Relative paths are resolved
	// against the directory of the configuration file.
	DataDir string `toml:"data_dir"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `toml:"log_level"`

	// FFprobe is the ffprobe binary, looked up in PATH unless absolute.
	FFprobe string `toml:"ffprobe"`
	// ProbeWorkers is the number of ffprobe processes run in parallel.
	ProbeWorkers int `toml:"probe_workers"`
	// ScanInterval is the time between automatic library scans; 0 turns
	// them off. A scan also runs at startup.
	ScanInterval Duration `toml:"scan_interval"`
	// SortArticles are leading words ignored when sorting by name.
	SortArticles []string `toml:"sort_articles"`
	// FFmpeg is the ffmpeg binary used for still images, looked up in PATH
	// unless absolute.
	FFmpeg string `toml:"ffmpeg"`
	// ImageWorkers is the number of ffmpeg processes extracting stills.
	ImageWorkers int `toml:"image_workers"`
	// DisplayLanguage is the language of stream titles: "en" or "de".
	DisplayLanguage string `toml:"display_language"`

	Libraries []Library `toml:"library"`
}

// Library is one movie library made of one or more directories.
type Library struct {
	Name  string   `toml:"name"`
	Paths []string `toml:"paths"`
}

// Duration is a time.Duration written as a string such as "6h" or "90m".
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Default returns the configuration used for keys the file does not set.
func Default() Config {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "homeflix"
	}
	return Config{
		ServerName:      name,
		Listen:          ":8096",
		DataDir:         "/var/db/homeflix",
		LogLevel:        "info",
		FFprobe:         "ffprobe",
		ProbeWorkers:    4,
		ScanInterval:    Duration{6 * time.Hour},
		SortArticles:    []string{"the", "a", "an"},
		FFmpeg:          "ffmpeg",
		ImageWorkers:    2,
		DisplayLanguage: "en",
	}
}

// Load reads the file at path on top of Default and validates the result.
func Load(path string) (Config, error) {
	cfg := Default()
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if cfg.DataDir != "" && !filepath.IsAbs(cfg.DataDir) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return Config{}, err
		}
		cfg.DataDir = filepath.Join(filepath.Dir(abs), cfg.DataDir)
	}
	for i := range cfg.Libraries {
		for j, p := range cfg.Libraries[i].Paths {
			cfg.Libraries[i].Paths[j] = filepath.Clean(p)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate reports the first invalid setting.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ServerName) == "" {
		return errors.New("server_name must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen %q: %w", c.Listen, err)
	}
	if c.DataDir == "" {
		return errors.New("data_dir must not be empty")
	}
	if _, err := c.SlogLevel(); err != nil {
		return err
	}
	if c.FFprobe == "" {
		return errors.New("ffprobe must not be empty")
	}
	if c.ProbeWorkers < 1 || c.ProbeWorkers > 64 {
		return fmt.Errorf("probe_workers %d: want 1 to 64", c.ProbeWorkers)
	}
	if c.ScanInterval.Duration < 0 {
		return errors.New("scan_interval must not be negative")
	}
	if c.FFmpeg == "" {
		return errors.New("ffmpeg must not be empty")
	}
	if c.ImageWorkers < 1 || c.ImageWorkers > 16 {
		return fmt.Errorf("image_workers %d: want 1 to 16", c.ImageWorkers)
	}
	if c.DisplayLanguage != "en" && c.DisplayLanguage != "de" {
		return fmt.Errorf("display_language %q: want en or de", c.DisplayLanguage)
	}
	names := map[string]bool{}
	paths := map[string]string{}
	for _, lib := range c.Libraries {
		if strings.TrimSpace(lib.Name) == "" {
			return errors.New("library: name must not be empty")
		}
		if names[strings.ToLower(lib.Name)] {
			return fmt.Errorf("library %q: defined twice", lib.Name)
		}
		names[strings.ToLower(lib.Name)] = true
		if len(lib.Paths) == 0 {
			return fmt.Errorf("library %q: paths must not be empty", lib.Name)
		}
		for _, p := range lib.Paths {
			if !filepath.IsAbs(p) {
				return fmt.Errorf("library %q: path %q must be absolute", lib.Name, p)
			}
			for other, owner := range paths {
				if p == other || strings.HasPrefix(p, other+"/") || strings.HasPrefix(other, p+"/") {
					return fmt.Errorf("library %q: path %q overlaps %q of library %q", lib.Name, p, other, owner)
				}
			}
			paths[p] = lib.Name
		}
	}
	return nil
}

// SlogLevel converts LogLevel to a slog.Level.
func (c Config) SlogLevel() (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return 0, fmt.Errorf("log_level %q: want debug, info, warn or error", c.LogLevel)
	}
	return l, nil
}
