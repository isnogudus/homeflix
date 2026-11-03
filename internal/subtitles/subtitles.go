// Package subtitles extracts embedded text subtitles as separate files,
// for clients that cannot read them from the container.
package subtitles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/media"
)

// ErrNotText is returned for subtitle streams that are images (PGS,
// DVD) or not subtitles at all.
var ErrNotText = errors.New("not a text subtitle stream")

// ErrFormat is returned for output formats homeflix does not write.
var ErrFormat = errors.New("unsupported subtitle format")

// format is an output format: ffmpeg codec and muxer, and content type.
type format struct {
	codec, muxer, contentType string
}

var formats = map[string]format{
	"srt": {"srt", "srt", "application/x-subrip; charset=utf-8"},
	"vtt": {"webvtt", "webvtt", "text/vtt; charset=utf-8"},
	"ass": {"ass", "ass", "text/x-ssa; charset=utf-8"},
	"ssa": {"ass", "ass", "text/x-ssa; charset=utf-8"},
}

// ContentType returns the content type of a format, or "".
func ContentType(f string) string { return formats[strings.ToLower(f)].contentType }

// extractTimeout bounds one extraction: ffmpeg reads the whole file.
const extractTimeout = 15 * time.Minute

// Service extracts and caches subtitle files.
type Service struct {
	FFmpeg string
	Dir    string
	Log    *slog.Logger

	mu       sync.Mutex
	inflight map[string]chan struct{}
	errs     map[string]error
}

// Get returns the path of stream index of part in format, extracting it
// first if needed. Concurrent requests for the same file share one run.
func (s *Service) Get(ctx context.Context, part catalog.Part, index int, f string) (string, error) {
	f = strings.ToLower(f)
	out, ok := formats[f]
	if !ok {
		return "", ErrFormat
	}
	var stream *media.Stream
	for i := range part.Info.Streams {
		if st := &part.Info.Streams[i]; st.Index == index {
			stream = st
		}
	}
	if stream == nil || stream.Type != "Subtitle" || !stream.IsTextSubtitleStream {
		return "", ErrNotText
	}
	path := filepath.Join(s.Dir, key(part), fmt.Sprintf("%d.%s", index, f))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	s.mu.Lock()
	if s.inflight == nil {
		s.inflight, s.errs = map[string]chan struct{}{}, map[string]error{}
	}
	if done, ok := s.inflight[path]; ok {
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			err := s.errs[path]
			s.mu.Unlock()
			return path, err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	done := make(chan struct{})
	s.inflight[path] = done
	s.mu.Unlock()

	err := s.extract(part.File.Path, index, out, path)
	s.mu.Lock()
	delete(s.inflight, path)
	if err != nil {
		s.errs[path] = err
	} else {
		delete(s.errs, path)
	}
	s.mu.Unlock()
	close(done)
	return path, err
}

// key names the cache directory of a file; it changes with the file.
func key(p catalog.Part) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", p.File.ID, p.File.Size, p.File.ModTime.UnixMilli())))
	return hex.EncodeToString(sum[:12])
}

// RemoveUnused deletes the extracted subtitles of files that are gone or
// have changed.
func (s *Service) RemoveUnused(movies []*catalog.Movie) {
	wanted := map[string]bool{}
	var add func(m *catalog.Movie)
	add = func(m *catalog.Movie) {
		for _, v := range m.Versions {
			for _, p := range v.Parts {
				wanted[key(p)] = true
			}
		}
		for _, x := range m.Extras {
			add(x)
		}
	}
	for _, m := range movies {
		add(m)
	}
	des, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	removed := 0
	for _, de := range des {
		if !wanted[de.Name()] && os.RemoveAll(filepath.Join(s.Dir, de.Name())) == nil {
			removed++
		}
	}
	if removed > 0 && s.Log != nil {
		s.Log.Info("removed unused subtitles", "files", removed)
	}
}

func (s *Service) extract(src string, index int, out format, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".extract-*")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	// Not bound to the request: other requests may wait for this file.
	ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, s.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "file:"+src, "-map", fmt.Sprintf("0:%d", index), "-c:s", out.codec, "-f", out.muxer, "-y", tmp.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("extract subtitles from %s: %s", src, msg)
	}
	if s.Log != nil {
		s.Log.Info("subtitles extracted", "path", src, "stream", index, "took", time.Since(start).Round(time.Millisecond))
	}
	return os.Rename(tmp.Name(), path)
}
