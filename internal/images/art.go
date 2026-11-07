package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"homeflix/internal/catalog"
)

// ArtTag identifies a picture of an album, artist or book, "" if there is
// none. It changes when the file holding the picture changes.
func ArtTag(a catalog.Art) string {
	if a.IsZero() {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("art|%s|%s|%d|%d", a.File, a.Embedded, a.Stream, a.Stamp)))
	return hex.EncodeToString(sum[:8])
}

// Art returns a JPEG file of a picture of music or a book, scaled as
// needed. Embedded pictures are taken out of their audio file once.
func (s *Service) Art(ctx context.Context, a catalog.Art, size Size) (string, error) {
	tag := ArtTag(a)
	if tag == "" {
		return "", ErrNoImage
	}
	src := a.File
	if src == "" {
		var err error
		if src, err = s.embedded(ctx, a, tag); err != nil {
			return "", err
		}
	}
	return s.scaled(src, tag, false, size)
}

// embedded extracts the picture embedded in an audio file.
func (s *Service) embedded(ctx context.Context, a catalog.Art, tag string) (string, error) {
	path := filepath.Join(s.Dir, "covers", tag+".jpg")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	s.init()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.%d.tmp.jpg", path, time.Now().UnixNano())
	runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "file:"+a.Embedded, "-map", fmt.Sprintf("0:%d", a.Stream), "-frames:v", "1",
		"-c:v", "mjpeg", "-q:v", "2", "-pix_fmt", "yuvj444p", "-f", "image2", "-y", tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("extract picture from %s: %s", a.Embedded, msg)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// artTags lists the tags of every picture of music and books, so that
// their cached files are kept.
func artTags(snap *catalog.Snapshot) []string {
	var tags []string
	add := func(a catalog.Art) {
		if t := ArtTag(a); t != "" {
			tags = append(tags, t)
		}
	}
	for _, al := range snap.Albums() {
		add(al.Cover)
	}
	for _, a := range snap.Artists() {
		add(a.Primary)
		add(a.Backdrop)
		add(a.Logo)
	}
	for _, t := range snap.Tracks() {
		if t.Track.Album == nil && t.Track.Meta.Cover >= 0 {
			f := t.Versions[0].Parts[0].File
			add(catalog.Art{Embedded: f.Path, Stream: t.Track.Meta.Cover, Stamp: f.ModTime.UnixMilli()})
		}
	}
	for _, b := range snap.Books() {
		if b.Book != nil {
			add(b.Book.Cover)
		}
	}
	return tags
}
