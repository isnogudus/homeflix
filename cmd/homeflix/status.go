package main

import (
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"homeflix/internal/config"
	"homeflix/internal/library"
	"homeflix/internal/web"
)

// scanTracker keeps the state of the library scans for the admin UI.
type scanTracker struct {
	mu sync.Mutex
	s  web.ScanStatus
}

func (t *scanTracker) start(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s.Running, t.s.Reason, t.s.Started = true, reason, time.Now()
}

func (t *scanTracker) finish(rep library.Report, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s.Running, t.s.Finished, t.s.Report, t.s.Err = false, time.Now(), rep, ""
	if err != nil {
		t.s.Err = err.Error()
	}
}

// cancel marks a scan that ended without a result, such as one skipped
// because another runs.
func (t *scanTracker) cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s.Running = false
}

func (t *scanTracker) status() web.ScanStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s
}

// settingsOf lists the configuration as the admin UI shows it, with the
// statement names of homeflix.conf. The TMDB token is left out.
func settingsOf(cfg config.Config, configPath string) []web.Setting {
	onOff := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	nets := func(ps []netip.Prefix) string {
		if len(ps) == 0 {
			return "none"
		}
		var out []string
		for _, p := range ps {
			if p.Bits() == p.Addr().BitLen() {
				out = append(out, p.Addr().String())
			} else {
				out = append(out, p.String())
			}
		}
		return strings.Join(out, ", ")
	}
	interval := func(d time.Duration) string {
		if d == 0 {
			return "off"
		}
		if d%(24*time.Hour) == 0 {
			return fmt.Sprintf("%dd", d/(24*time.Hour))
		}
		return d.String()
	}
	out := []web.Setting{
		{Name: "configuration file", Value: configPath},
		{Name: "server name", Value: cfg.ServerName},
		{Name: "listen on", Value: cfg.Listen},
		{Name: "discovery", Value: onOff(cfg.Discovery)},
		{Name: "trusted proxy", Value: nets(cfg.TrustedProxies)},
		{Name: "admin from", Value: nets(cfg.AdminFrom)},
		{Name: "data directory", Value: cfg.DataDir},
		{Name: "log level", Value: cfg.LogLevel},
		{Name: "language", Value: cfg.DisplayLanguage},
		{Name: "ffprobe", Value: cfg.FFprobe},
		{Name: "ffmpeg", Value: cfg.FFmpeg},
		{Name: "probe workers", Value: fmt.Sprint(cfg.ProbeWorkers)},
		{Name: "image workers", Value: fmt.Sprint(cfg.ImageWorkers)},
		{Name: "scan interval", Value: interval(cfg.ScanInterval)},
		{Name: "sort articles", Value: strings.Join(cfg.SortArticles, ", ")},
	}
	for _, l := range cfg.Libraries {
		out = append(out, web.Setting{Name: "library " + l.Name, Value: strings.Join(l.Paths, ", ")})
	}
	if t := cfg.TMDB; t != nil {
		out = append(out, web.Setting{Name: "tmdb token file", Value: t.TokenFile},
			web.Setting{Name: "tmdb language", Value: t.Language},
			web.Setting{Name: "tmdb refresh interval", Value: interval(t.RefreshInterval)})
	} else {
		out = append(out, web.Setting{Name: "tmdb", Value: "off"})
	}
	return out
}
