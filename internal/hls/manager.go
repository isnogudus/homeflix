package hls

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"homeflix/internal/keyframes"
)

// ErrBusy is returned when all ffmpeg workers are taken.
var ErrBusy = errors.New("all remux workers are busy")

// Source is one audio track of a file to remux.
type Source struct {
	Key      string // changes with the file's content, e.g. ID, size, mtime
	FileID   string
	Path     string
	Size     int64
	ModTime  time.Time
	Duration time.Duration
	Audio    int // stream index of the audio track
}

func (s Source) session() string { return s.Key + "-a" + strconv.Itoa(s.Audio) }

// Tuning of the sessions.
const (
	// ffmpeg pauses when this many segments are made ahead of the player
	// and resumes when half of them are watched.
	aheadSegments = 10
	// Segments this far behind the player are deleted; seeking back makes
	// them again.
	keepBehind = 5
	// A player that asks for a segment this far beyond what is made gets a
	// new ffmpeg run starting there instead of waiting. Players ask for a
	// few segments ahead after a seek; remuxing makes them in a second or
	// two, so waiting is better than starting over.
	restartGap = 8
	// Sessions without requests for this long end.
	idleTimeout = 2 * time.Minute
	// How long a request waits for its segment.
	segmentWait = 45 * time.Second
	// The fixed timestamp offset of the output: keeps decode times positive
	// at the start, so that every run has the same timestamps.
	tsOffset = 10 * time.Second
)

// Manager runs the remux sessions.
type Manager struct {
	FFmpeg  string
	Dir     string // working directory, emptied on Run
	Workers int    // ffmpeg processes at most
	Log     *slog.Logger
	// Keyframes returns the keyframes of a file, normally from the stored
	// index; if nil, they are read from the container index of the file.
	Keyframes func(ctx context.Context, src Source) ([]time.Duration, error)

	mu       sync.Mutex
	sessions map[string]*session
	plans    map[string]plan // by Source.Key
}

type plan struct {
	keyframes []time.Duration
	segments  []Segment
}

type session struct {
	src     Source
	plan    plan
	dir     string
	lastReq int          // segment the player asked for last
	seen    time.Time    // last request
	made    map[int]bool // finished segments on disk, from all runs
	run     *run         // the latest ffmpeg run, nil if none
}

// run is one ffmpeg process making segments from first on.
type run struct {
	first   int
	next    int // the next segment it will finish
	cmd     *exec.Cmd
	list    string        // ffmpeg's list of finished segments
	done    chan struct{} // closed when ffmpeg ended
	err     error         // set before done is closed
	paused  bool
	stopped atomic.Bool
}

func (m *Manager) keyframesOf(ctx context.Context, src Source) ([]time.Duration, error) {
	if m.Keyframes != nil {
		return m.Keyframes(ctx, src)
	}
	return keyframes.Read(src.Path)
}

// maxPlans bounds the cached segment plans.
const maxPlans = 512

// Segments plans the segments of a file, once per Source.Key.
func (m *Manager) Segments(ctx context.Context, src Source) ([]Segment, error) {
	p, err := m.planOf(ctx, src)
	return p.segments, err
}

func (m *Manager) planOf(ctx context.Context, src Source) (plan, error) {
	m.mu.Lock()
	p, ok := m.plans[src.Key]
	m.mu.Unlock()
	if ok {
		return p, nil
	}
	kf, err := m.keyframesOf(ctx, src)
	if err != nil {
		return plan{}, err
	}
	p = plan{keyframes: kf, segments: PlanSegments(kf, src.Duration)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plans == nil || len(m.plans) >= maxPlans {
		m.plans = map[string]plan{}
	}
	m.plans[src.Key] = p
	return p, nil
}

// Segment returns the path of segment n of src once ffmpeg has made it,
// starting or moving ffmpeg as needed.
func (m *Manager) Segment(ctx context.Context, src Source, n int) (string, error) {
	p, err := m.planOf(ctx, src)
	if err != nil {
		return "", err
	}
	if n < 0 || n >= len(p.segments) {
		return "", fmt.Errorf("segment %d of %d", n, len(p.segments))
	}
	deadline := time.Now().Add(segmentWait)
	for {
		path, ready, err := m.step(src, p, n)
		if err != nil || ready {
			return path, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("segment %d not ready after %v", n, segmentWait)
		}
	}
}

// step looks once whether segment n is there, and starts ffmpeg if no run
// will make it soon.
func (m *Manager) step(src Source, p plan, n int) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]*session{}
	}
	key := src.session()
	s, ok := m.sessions[key]
	if !ok {
		s = &session{src: src, plan: p, dir: filepath.Join(m.Dir, key), made: map[int]bool{}}
		m.sessions[key] = s
	}
	s.lastReq, s.seen = n, time.Now()
	path := filepath.Join(s.dir, strconv.Itoa(n)+".ts")
	r := s.run
	s.refresh()
	if s.made[n] {
		m.pace(s)
		return path, true, nil
	}
	if r != nil && n >= r.first && n >= r.next {
		switch {
		case r.exited() && r.err != nil && !r.killed():
			return "", false, r.err
		case !r.exited() && n <= r.next+restartGap:
			// It comes soon.
			m.pace(s)
			return path, false, nil
		}
	}
	if r != nil {
		r.stop()
	} else if m.running() >= max(1, m.Workers) {
		return "", false, ErrBusy
	}
	// The new run rewrites the segments from n on.
	for k := range s.made {
		if k >= n {
			delete(s.made, k)
		}
	}
	nr, err := m.start(s, n)
	if err != nil {
		return "", false, err
	}
	s.run = nr
	return path, false, nil
}

// running counts the sessions with a live ffmpeg.
func (m *Manager) running() int {
	n := 0
	for _, s := range m.sessions {
		if s.run != nil && !s.run.exited() {
			n++
		}
	}
	return n
}

// start runs ffmpeg for s from segment first on.
func (m *Manager) start(s *session, first int) (*run, error) {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return nil, err
	}
	segs := s.plan.segments
	start := segs[first].Start
	// Seek a little past the keyframe the segment starts at: an input seek
	// lands on the keyframe at or before its target, and the keyframe's
	// own timestamp may lie a frame after the time in the index.
	seek := start
	if first > 0 {
		eps := 200 * time.Millisecond
		for _, k := range s.plan.keyframes {
			if k > start {
				eps = min(eps, (k-start)/2)
				break
			}
		}
		seek += eps
	}
	var times []string
	for _, seg := range segs[first+1:] {
		times = append(times, seconds(seg.Start-start))
	}
	list := filepath.Join(s.dir, fmt.Sprintf("list-%d.csv", first))
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-ss", seconds(seek), "-copyts", "-i", "file:" + s.src.Path,
		"-map", "0:v:0", "-map", "0:" + strconv.Itoa(s.src.Audio),
		"-c:v", "copy", "-c:a", "aac", "-ac", "2", "-b:a", "192k",
		"-output_ts_offset", seconds(tsOffset),
		"-f", "segment", "-segment_format", "mpegts", "-segment_format_options", "mpegts_copyts=1",
		"-segment_start_number", strconv.Itoa(first),
		"-segment_list", list, "-segment_list_type", "csv",
		"-muxdelay", "0", "-muxpreload", "0"}
	if len(times) > 0 {
		args = append(args, "-segment_times", strings.Join(times, ","))
	}
	args = append(args, filepath.Join(s.dir, "%d.ts"))
	cmd := exec.Command(m.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := &run{first: first, next: first, cmd: cmd, list: list, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		if err != nil && !r.killed() {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			r.err = fmt.Errorf("ffmpeg: %s", msg)
			if m.Log != nil {
				m.Log.Warn("remux failed", "path", s.src.Path, "segment", first, "err", msg)
			}
		}
		close(r.done)
	}()
	if m.Log != nil {
		m.Log.Info("remux started", "path", s.src.Path, "audio", s.src.Audio, "segment", first,
			"at", start.Round(time.Second))
	}
	return r, nil
}

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) }

// pace pauses ffmpeg when it is far ahead of the player and resumes it
// when the player catches up.
func (m *Manager) pace(s *session) {
	r := s.run
	if r == nil || r.exited() {
		return
	}
	ahead := r.next - s.lastReq
	switch {
	case !r.paused && ahead > aheadSegments:
		if r.cmd.Process.Signal(syscall.SIGSTOP) == nil {
			r.paused = true
		}
	case r.paused && ahead <= aheadSegments/2:
		if r.cmd.Process.Signal(syscall.SIGCONT) == nil {
			r.paused = false
		}
	}
}

func (r *run) exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// refresh reads the latest run's list of finished segments.
func (s *session) refresh() {
	r := s.run
	if r == nil {
		return
	}
	f, err := os.Open(r.list)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, _, _ := strings.Cut(sc.Text(), ",")
		if n, err := strconv.Atoi(strings.TrimSuffix(name, ".ts")); err == nil {
			s.made[n] = true
			r.next = max(r.next, n+1)
		}
	}
}

func (r *run) killed() bool { return r.stopped.Load() }

// stop ends the run; a paused ffmpeg is woken so that it can die.
func (r *run) stop() {
	if r.exited() {
		return
	}
	r.stopped.Store(true)
	r.cmd.Process.Signal(syscall.SIGCONT)
	r.cmd.Process.Kill()
	<-r.done
}

// Run empties the working directory and then, until ctx is done, ends
// idle sessions and deletes segments the players have passed.
func (m *Manager) Run(ctx context.Context) {
	os.RemoveAll(m.Dir)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for key, s := range m.sessions {
				m.end(key, s)
			}
			m.mu.Unlock()
			return
		case <-t.C:
			m.sweep()
		}
	}
}

func (m *Manager) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, s := range m.sessions {
		if time.Since(s.seen) > idleTimeout {
			m.end(key, s)
			continue
		}
		s.refresh()
		m.pace(s)
		des, err := os.ReadDir(s.dir)
		if err != nil {
			continue
		}
		for _, de := range des {
			n, err := strconv.Atoi(strings.TrimSuffix(de.Name(), ".ts"))
			if err == nil && strings.HasSuffix(de.Name(), ".ts") && n < s.lastReq-keepBehind {
				os.Remove(filepath.Join(s.dir, de.Name()))
				delete(s.made, n)
			}
		}
	}
}

// end stops a session and deletes its segments.
func (m *Manager) end(key string, s *session) {
	if s.run != nil {
		s.run.stop()
	}
	os.RemoveAll(s.dir)
	delete(m.sessions, key)
	if m.Log != nil {
		m.Log.Info("remux ended", "path", s.src.Path)
	}
}

// Sessions is the number of sessions, for the admin UI.
func (m *Manager) Sessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}
