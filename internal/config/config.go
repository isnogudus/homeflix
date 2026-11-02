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
}

// Default returns the configuration used for keys the file does not set.
func Default() Config {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "homeflix"
	}
	return Config{
		ServerName: name,
		Listen:     ":8096",
		DataDir:    "/var/db/homeflix",
		LogLevel:   "info",
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
