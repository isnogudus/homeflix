// Package images makes artwork from the videos themselves: one still per
// movie, cut and resized on demand, cached on disk.
package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"

	"homeflix/internal/catalog"
	"homeflix/internal/media"
)

// Kind is a Jellyfin image type.
type Kind string

// Image types homeflix serves.
const (
	Primary  Kind = "Primary"
	Backdrop Kind = "Backdrop"
	Thumb    Kind = "Thumb"
)

// ParseKind accepts Jellyfin image type names in any case.
func ParseKind(s string) (Kind, bool) {
	for _, k := range []Kind{Primary, Backdrop, Thumb} {
		if strings.EqualFold(s, string(k)) {
			return k, true
		}
	}
	return "", false
}

// PosterAspect is width over height of the posters cut from stills.
const PosterAspect = 2.0 / 3.0

// stillWidth caps the width of extracted stills.
const stillWidth = 1280

// candidatePositions are the points, as fractions of the runtime, where
// candidate frames are taken; the best of them becomes the still.
var candidatePositions = []float64{0.10, 0.30, 0.50}

// ErrNoImage is returned for items without video.
var ErrNoImage = errors.New("no image")

// Size describes the requested output, in Jellyfin's query terms.
type Size struct {
	MaxWidth, MaxHeight   int
	Width, Height         int
	FillWidth, FillHeight int
	Quality               int // 1–100, default 90
}

// Service extracts and scales images.
type Service struct {
	FFmpeg  string
	Dir     string // cache directory
	Workers int    // ffmpeg processes at a time
	Log     *slog.Logger

	once     sync.Once
	sem      chan struct{}
	mu       sync.Mutex
	inflight map[string]*call
}

type call struct {
	done chan struct{}
	err  error
}

func (s *Service) init() {
	s.once.Do(func() {
		n := s.Workers
		if n < 1 {
			n = 1
		}
		s.sem = make(chan struct{}, n)
		s.inflight = map[string]*call{}
	})
}

// source picks the file and video stream a still is taken from.
func source(m *catalog.Movie) (catalog.Part, media.Stream, bool) {
	if len(m.Versions) == 0 {
		return catalog.Part{}, media.Stream{}, false
	}
	p := m.Versions[0].Parts[0]
	v, ok := p.Info.Video()
	return p, v, ok
}

// artwork returns the TMDB image file for a kind, if the movie has one.
func artwork(m *catalog.Movie, kind Kind) string {
	if m.Meta == nil || m.IsExtra() {
		return ""
	}
	if kind == Primary {
		return m.Meta.PosterFile
	}
	return m.Meta.BackdropFile
}

// Tag identifies the current image of a kind; it changes when the source
// changes, so clients may cache by it forever.
func Tag(m *catalog.Movie, kind Kind) string {
	if kind == Thumb {
		kind = Backdrop
	}
	if a := artwork(m, kind); a != "" {
		sum := sha256.Sum256([]byte("tmdb|" + filepath.Base(a) + "|" + string(kind)))
		return hex.EncodeToString(sum[:8])
	}
	return frameTag(m, kind)
}

// frameTag identifies an image made from the movie's still.
func frameTag(m *catalog.Movie, kind Kind) string {
	p, _, ok := source(m)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s|v2", p.File.ID, p.File.Size, p.File.ModTime.UnixMilli(), kind)))
	return hex.EncodeToString(sum[:8])
}

// stillTag names the still images without artwork are made from.
func stillTag(m *catalog.Movie) string { return frameTag(m, Backdrop) }

// AspectRatio is width over height of the primary image.
func AspectRatio(m *catalog.Movie) float64 {
	if !m.IsExtra() {
		return PosterAspect
	}
	if _, v, ok := source(m); ok && v.Height > 0 {
		return float64(v.Width) / float64(v.Height)
	}
	return 16.0 / 9.0
}

func seekPositions(d time.Duration) []time.Duration {
	if d <= 0 {
		return []time.Duration{time.Minute}
	}
	out := make([]time.Duration, len(candidatePositions))
	for i, f := range candidatePositions {
		out[i] = time.Duration(float64(d) * f)
	}
	return out
}

func (s *Service) stillPath(tag string) string {
	return filepath.Join(s.Dir, "stills", tag+".jpg")
}

// still returns the path of the still of m, making it first if needed.
// Concurrent requests for the same still share one ffmpeg run.
func (s *Service) still(ctx context.Context, m *catalog.Movie) (string, error) {
	s.init()
	p, v, ok := source(m)
	if !ok {
		return "", ErrNoImage
	}
	path := s.stillPath(stillTag(m))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	s.mu.Lock()
	if c, ok := s.inflight[path]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return path, c.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	s.inflight[path] = c
	s.mu.Unlock()

	c.err = s.extract(ctx, p, v, path)
	s.mu.Lock()
	delete(s.inflight, path)
	s.mu.Unlock()
	close(c.done)
	return path, c.err
}

// extract takes candidate frames with one ffmpeg run, converts HDR10
// frames to SDR, trims black bars and keeps the best frame as JPEG.
func (s *Service) extract(ctx context.Context, p catalog.Part, v media.Stream, path string) error {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	tmpDir, err := os.MkdirTemp(s.Dir, ".extract-")
	if err != nil {
		if err := os.MkdirAll(s.Dir, 0o750); err != nil {
			return err
		}
		if tmpDir, err = os.MkdirTemp(s.Dir, ".extract-"); err != nil {
			return err
		}
	}
	defer os.RemoveAll(tmpDir)

	pq := strings.EqualFold(v.ColorTransfer, "smpte2084")
	conv := ""
	if pq {
		// Keep the PQ signal as it is; toneMapPQ interprets it.
		conv = ":in_color_matrix=bt2020:in_range=tv:out_range=pc"
	}
	vf := fmt.Sprintf("scale=w='trunc(min(%d\\,iw*sar)/2)*2':h='trunc(ow/dar/2)*2'%s,setsar=1,format=rgb48be", stillWidth, conv)
	positions := seekPositions(p.Info.Duration)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	for _, at := range positions {
		args = append(args, "-ss", fmt.Sprintf("%.3f", at.Seconds()), "-i", "file:"+p.File.Path)
	}
	var outs []string
	for i := range positions {
		out := filepath.Join(tmpDir, fmt.Sprintf("%d.png", i))
		outs = append(outs, out)
		args = append(args, "-map", fmt.Sprintf("%d:%d", i, v.Index), "-frames:v", "1", "-vf", vf,
			"-c:v", "png", "-f", "image2", "-y", out)
	}
	// Not bound to the request: other requests may be waiting for this
	// still, and a client that goes away must not abort it for them.
	runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()

	var best image.Image
	bestScore := -1.0
	for _, out := range outs {
		img, err := decodeFile(out)
		if err != nil {
			continue
		}
		if pq {
			img = toneMapPQ(img)
		}
		img = trimBars(img)
		if sc := score(img); sc > bestScore {
			best, bestScore = img, sc
		}
	}
	if best == nil {
		msg := strings.TrimSpace(stderr.String())
		switch {
		case runErr != nil && msg == "":
			msg = runErr.Error()
		case msg == "":
			msg = "no frame decoded"
		}
		return fmt.Errorf("extract still from %s: %s", p.File.Path, msg)
	}
	if err := writeJPEG(path, best, 90); err != nil {
		return err
	}
	if s.Log != nil {
		s.Log.Debug("still extracted", "path", p.File.Path, "hdr", pq, "took", time.Since(start).Round(time.Millisecond))
	}
	return nil
}

func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// writeJPEG writes img atomically: concurrent writers of the same path do
// not see each other's partial files.
func writeJPEG(path string, img image.Image, quality int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns a JPEG file for the requested image: TMDB artwork if there
// is some, else made from the still, cut and scaled as needed.
func (s *Service) Get(ctx context.Context, m *catalog.Movie, kind Kind, size Size) (string, error) {
	if kind == Thumb {
		kind = Backdrop
	}
	src := artwork(m, kind)
	poster := false
	if src == "" {
		still, err := s.still(ctx, m)
		if err != nil {
			return "", err
		}
		src, poster = still, kind == Primary && !m.IsExtra()
	}
	return s.scaled(src, Tag(m, kind), poster, size)
}

// PhotoTag identifies the portrait of a person, "" if there is none.
func PhotoTag(f *catalog.Facet) string {
	if f.Person == nil || f.Person.PhotoFile == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("tmdb|" + filepath.Base(f.Person.PhotoFile) + "|Person"))
	return hex.EncodeToString(sum[:8])
}

// Photo returns a JPEG file of a person's portrait, scaled as needed.
func (s *Service) Photo(f *catalog.Facet, size Size) (string, error) {
	tag := PhotoTag(f)
	if tag == "" {
		return "", ErrNoImage
	}
	return s.scaled(f.Person.PhotoFile, tag, false, size)
}

// scaled returns src scaled to size, cut to the poster shape if poster is
// set, from the cache of tag if it was made before.
func (s *Service) scaled(src, tag string, poster bool, size Size) (string, error) {
	q := size.Quality
	if q <= 0 || q > 100 {
		q = 90
	}
	variant := fmt.Sprintf("%t-%d-%d-%d-%d-%d-%d-%d", poster, size.MaxWidth, size.MaxHeight,
		size.Width, size.Height, size.FillWidth, size.FillHeight, q)
	sum := sha256.Sum256([]byte(variant))
	path := filepath.Join(s.Dir, "scaled", tag, hex.EncodeToString(sum[:12])+".jpg")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	img, err := decodeFile(src)
	if err != nil {
		return "", err
	}
	if poster {
		img = cropToAspect(img, PosterAspect)
	}
	return path, writeJPEG(path, scale(img, size), q)
}

// cropToAspect cuts the centre of img to the given width/height ratio.
func cropToAspect(img image.Image, aspect float64) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cw, ch := w, h
	if float64(w)/float64(h) > aspect {
		cw = int(float64(h) * aspect)
	} else {
		ch = int(float64(w) / aspect)
	}
	x0 := b.Min.X + (w-cw)/2
	y0 := b.Min.Y + (h-ch)/2
	dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Copy(dst, image.Point{}, img, image.Rect(x0, y0, x0+cw, y0+ch), draw.Src, nil)
	return dst
}

// targetSize computes the output size for a source of w×h. Fill sizes cut
// the image to their aspect ratio first; images are never enlarged.
func targetSize(w, h int, s Size) (cropAspect float64, tw, th int) {
	if s.FillWidth > 0 && s.FillHeight > 0 {
		cropAspect = float64(s.FillWidth) / float64(s.FillHeight)
		if float64(w)/float64(h) > cropAspect {
			w = int(float64(h) * cropAspect)
		} else {
			h = int(float64(w) / cropAspect)
		}
		tw, th = min(s.FillWidth, w), min(s.FillHeight, h)
		return cropAspect, tw, th
	}
	maxW, maxH := s.MaxWidth, s.MaxHeight
	if s.Width > 0 && (maxW == 0 || s.Width < maxW) {
		maxW = s.Width
	}
	if s.Height > 0 && (maxH == 0 || s.Height < maxH) {
		maxH = s.Height
	}
	if s.FillWidth > 0 && (maxW == 0 || s.FillWidth < maxW) {
		maxW = s.FillWidth
	}
	if s.FillHeight > 0 && (maxH == 0 || s.FillHeight < maxH) {
		maxH = s.FillHeight
	}
	f := 1.0
	if maxW > 0 && w > maxW {
		f = min(f, float64(maxW)/float64(w))
	}
	if maxH > 0 && h > maxH {
		f = min(f, float64(maxH)/float64(h))
	}
	return 0, max(1, int(float64(w)*f+0.5)), max(1, int(float64(h)*f+0.5))
}

func scale(img image.Image, s Size) image.Image {
	b := img.Bounds()
	aspect, tw, th := targetSize(b.Dx(), b.Dy(), s)
	if aspect > 0 {
		img = cropToAspect(img, aspect)
		b = img.Bounds()
	}
	if tw == b.Dx() && th == b.Dy() {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// Warm makes the stills of all movies and extras that have none yet, so
// browsing does not wait for ffmpeg, and removes cached images no movie or
// person uses any more. It stops when ctx is done.
func (s *Service) Warm(ctx context.Context, movies []*catalog.Movie, people []*catalog.Facet) {
	s.init()
	wanted := map[string]bool{}
	for _, f := range people {
		if t := PhotoTag(f); t != "" {
			wanted[t] = true
		}
	}
	var todo []*catalog.Movie
	consider := func(m *catalog.Movie) {
		for _, k := range []Kind{Primary, Backdrop} {
			if t := Tag(m, k); t != "" {
				wanted[t] = true
			}
		}
		tag := stillTag(m)
		if tag == "" {
			return
		}
		wanted[tag] = true
		if artwork(m, Primary) != "" && artwork(m, Backdrop) != "" {
			return
		}
		if _, err := os.Stat(s.stillPath(tag)); err != nil {
			todo = append(todo, m)
		}
	}
	for _, m := range movies {
		consider(m)
		for _, x := range m.Extras {
			consider(x)
		}
	}
	s.removeUnused(wanted)
	if len(todo) == 0 {
		return
	}
	if s.Log != nil {
		s.Log.Info("extracting stills", "count", len(todo))
	}
	start, failed := time.Now(), 0
	var wg sync.WaitGroup
	var mu sync.Mutex
	jobs := make(chan *catalog.Movie)
	for range cap(s.sem) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range jobs {
				if _, err := s.still(ctx, m); err != nil && ctx.Err() == nil {
					mu.Lock()
					failed++
					mu.Unlock()
					if s.Log != nil {
						s.Log.Warn("still extraction failed", "item", m.Title, "err", err)
					}
				}
			}
		}()
	}
feed:
	for _, m := range todo {
		select {
		case jobs <- m:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if s.Log != nil && ctx.Err() == nil {
		s.Log.Info("stills extracted", "count", len(todo), "failed", failed, "took", time.Since(start).Round(time.Second))
	}
}

// removeUnused deletes stills and scaled images whose tag is not wanted.
func (s *Service) removeUnused(wanted map[string]bool) {
	removed := 0
	if des, err := os.ReadDir(filepath.Join(s.Dir, "stills")); err == nil {
		for _, de := range des {
			tag := strings.TrimSuffix(de.Name(), ".jpg")
			if !de.IsDir() && strings.HasSuffix(de.Name(), ".jpg") && !wanted[tag] {
				if os.Remove(filepath.Join(s.Dir, "stills", de.Name())) == nil {
					removed++
				}
			}
		}
	}
	if des, err := os.ReadDir(filepath.Join(s.Dir, "scaled")); err == nil {
		for _, de := range des {
			if !de.IsDir() || !wanted[de.Name()] {
				if os.RemoveAll(filepath.Join(s.Dir, "scaled", de.Name())) == nil {
					removed++
				}
			}
		}
	}
	if removed > 0 && s.Log != nil {
		s.Log.Info("removed unused images", "count", removed)
	}
}
