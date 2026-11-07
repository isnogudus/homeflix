package hls

import (
	"bufio"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"homeflix/internal/keyframes"
)

// ErrBusy is returned when all ffmpeg workers are taken.
var ErrBusy = errors.New("all conversion workers are busy")

// Source is one audio track of a file to convert, with its video copied
// or transcoded.
type Source struct {
	Key      string // changes with the file's content, e.g. ID, size, mtime
	FileID   string
	Path     string
	Size     int64
	ModTime  time.Time
	Duration time.Duration
	Audio    int    // stream index of the audio track
	Video    *Video // how the video is transcoded; nil copies it
	// AudioOnly converts the audio track alone, for music and
	// audiobooks; Video is then ignored.
	AudioOnly bool
	// Concat, if set, are files played one after another as one stream
	// (an audiobook of several files); Path is then unused for input.
	Concat []string
	// Owner is the player asking, such as a web session. A player plays
	// one thing at a time, so its new session ends its others; players
	// do not share sessions.
	Owner string
}

func (s Source) session() string {
	k := s.Key + "-a" + strconv.Itoa(s.Audio)
	if s.AudioOnly {
		k += "-audio"
	} else {
		k += s.Video.key()
	}
	if s.Owner != "" {
		k += "-o" + s.Owner
	}
	return k
}

func (s Source) verb() string {
	if s.AudioOnly {
		return "audio"
	}
	if s.Video != nil {
		return "transcode"
	}
	return "remux"
}

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
	// two, so waiting is better than starting over. Transcoding is slower,
	// so it starts over sooner.
	restartGap          = 8
	restartGapTranscode = 3
	// Sessions without requests for this long end.
	idleTimeout = 2 * time.Minute
	// When all workers are taken, the ffmpeg of a session without requests
	// for this long gives way to a new one: its player is paused or gone,
	// or it switched to another variant. Its segments stay; it gets a
	// worker again when it asks.
	yieldAfter = 30 * time.Second
	// How long a request waits for its segment.
	segmentWait = 45 * time.Second
	// The fixed timestamp offset of the output: keeps decode times positive
	// at the start, so that every run has the same timestamps.
	tsOffset = 10 * time.Second
)

// Manager runs the conversion sessions.
type Manager struct {
	FFmpeg       string
	Dir          string // working directory, emptied on Run
	Workers      int    // ffmpeg processes at most
	VideoWorkers int    // of them, transcoding video at most
	Log          *slog.Logger
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

// Segments plans the segments of a file: for copied video once per
// Source.Key from its keyframes, for transcoded video at fixed times.
func (m *Manager) Segments(ctx context.Context, src Source) ([]Segment, error) {
	p, err := m.planOf(ctx, src)
	return p.segments, err
}

func (m *Manager) planOf(ctx context.Context, src Source) (plan, error) {
	if src.Video != nil || src.AudioOnly {
		return plan{segments: FixedSegments(src.Duration)}, nil
	}
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
		if src.Owner != "" {
			for k, o := range m.sessions {
				if o.src.Owner == src.Owner {
					m.end(k, o)
				}
			}
		}
		s = &session{src: src, plan: p, dir: filepath.Join(m.Dir, dirName(key)), made: map[int]bool{}}
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
	gap := restartGap
	if src.Video != nil {
		gap = restartGapTranscode
	}
	if r != nil && n >= r.first && n >= r.next {
		switch {
		case r.exited() && r.err != nil && !r.killed():
			return "", false, r.err
		case !r.exited() && n <= r.next+gap:
			// It comes soon.
			m.pace(s)
			return path, false, nil
		}
	}
	if r != nil && !r.exited() {
		r.stop()
	} else if !m.free(src.Video != nil) {
		if r == nil {
			// Nothing to keep: the player tries again later.
			delete(m.sessions, key)
		}
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

// free reports whether a new run, transcoding video or not, may start,
// stopping the run of an idle session if that makes room.
func (m *Manager) free(video bool) bool {
	full := func() bool {
		all, v := m.running()
		return all >= max(1, m.Workers) || video && v >= max(1, m.VideoWorkers)
	}
	for full() {
		var idle *session
		for _, s := range m.sessions {
			live := s.run != nil && !s.run.exited()
			// A remux gives way only where all workers are taken.
			if all, _ := m.running(); live && time.Since(s.seen) > yieldAfter && (s.src.Video != nil || all >= max(1, m.Workers)) &&
				(idle == nil || s.seen.Before(idle.seen)) {
				idle = s
			}
		}
		if idle == nil {
			return false
		}
		idle.run.stop()
		if m.Log != nil {
			m.Log.Info(idle.src.verb()+" yields its worker", "path", idle.src.Path, "idle", time.Since(idle.seen).Round(time.Second))
		}
	}
	return true
}

// running counts the sessions with a live ffmpeg, and of them those that
// transcode video.
func (m *Manager) running() (all, video int) {
	for _, s := range m.sessions {
		if s.run != nil && !s.run.exited() {
			all++
			if s.src.Video != nil {
				video++
			}
		}
	}
	return all, video
}

// start runs ffmpeg for s from segment first on.
func (m *Manager) start(s *session, first int) (*run, error) {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return nil, err
	}
	// An earlier run from the same segment left its list; it must not count
	// for this one.
	list := filepath.Join(s.dir, fmt.Sprintf("list-%d.csv", first))
	if err := os.Remove(list); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(s.src.Concat) > 0 {
		if err := writeConcatList(filepath.Join(s.dir, "concat.txt"), s.src.Concat); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(m.FFmpeg, s.args(first, list)...)
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
				m.Log.Warn(s.src.verb()+" failed", "path", s.src.Path, "segment", first, "err", msg)
			}
		}
		close(r.done)
	}()
	if m.Log != nil {
		attrs := []any{"path", s.src.Path, "audio", s.src.Audio, "segment", first,
			"at", s.plan.segments[first].Start.Round(time.Second)}
		if v := s.src.Video; v != nil {
			attrs = append(attrs, "size", fmt.Sprintf("%dx%d", v.Width, v.Height), "tonemap", v.ToneMap,
				"deinterlace", v.Deinterlace, "subtitle", v.Subtitle)
		}
		m.Log.Info(s.src.verb()+" started", attrs...)
	}
	return r, nil
}

// args are the ffmpeg arguments of a run from segment first on, writing
// its list of finished segments to list.
//
// Both kinds keep the timestamps of the file (-copyts), shifted by
// tsOffset, so that segments from different runs fit together. Copied
// video is cut at the keyframes of the plan; transcoded video gets a
// keyframe every TargetDuration from where the run starts, which are the
// segment starts of the fixed plan.
func (s *session) args(first int, list string) []string {
	segs := s.plan.segments
	start := segs[first].Start
	seek := start
	if s.src.Video == nil && !s.src.AudioOnly && first > 0 {
		// Seek a little past the keyframe the segment starts at: an input
		// seek lands on the keyframe at or before its target, and the
		// keyframe's own timestamp may lie a frame after the time in the
		// index. A transcode decodes up to the exact time instead.
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
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-ss", seconds(seek), "-copyts"}
	if len(s.src.Concat) > 0 {
		args = append(args, "-f", "concat", "-safe", "0", "-i", "file:"+filepath.Join(s.dir, "concat.txt"))
	} else {
		args = append(args, "-i", "file:"+s.src.Path)
	}
	if s.src.AudioOnly {
		// Music and audiobooks: the audio alone, stereo AAC at a rate
		// that keeps music transparent.
		audio := "0:" + strconv.Itoa(s.src.Audio)
		if len(s.src.Concat) > 0 {
			audio = "0:a:0"
		}
		args = append(args, "-map", audio, "-vn", "-sn", "-dn",
			"-c:a", "aac", "-ac", "2", "-b:a", "256k",
			"-output_ts_offset", seconds(tsOffset),
			"-f", "segment", "-segment_format", "mpegts", "-segment_format_options", "mpegts_copyts=1",
			"-segment_start_number", strconv.Itoa(first),
			"-segment_list", list, "-segment_list_type", "csv",
			"-muxdelay", "0", "-muxpreload", "0")
		if len(times) > 0 {
			args = append(args, "-segment_times", strings.Join(times, ","))
		}
		return append(args, filepath.Join(s.dir, "%d.ts"))
	}
	if v := s.src.Video; v != nil {
		args = append(args, "-filter_complex", v.filter(), "-map", "[v]", "-map", "0:"+strconv.Itoa(s.src.Audio))
		args = append(args, x264Args...)
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", TargetDuration.Seconds()))
	} else {
		args = append(args, "-map", "0:v:0", "-map", "0:"+strconv.Itoa(s.src.Audio), "-c:v", "copy")
	}
	args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k",
		"-output_ts_offset", seconds(tsOffset),
		"-f", "segment", "-segment_format", "mpegts", "-segment_format_options", "mpegts_copyts=1",
		"-segment_start_number", strconv.Itoa(first),
		"-segment_list", list, "-segment_list_type", "csv",
		"-muxdelay", "0", "-muxpreload", "0")
	if len(times) > 0 {
		args = append(args, "-segment_times", strings.Join(times, ","))
	}
	return append(args, filepath.Join(s.dir, "%d.ts"))
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

// refresh reads the latest run's list of finished segments. Only the
// segments new since the last reading count: those read before may have
// been deleted behind the player since.
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
		if n, err := strconv.Atoi(strings.TrimSuffix(name, ".ts")); err == nil && n >= r.next {
			s.made[n] = true
			r.next = n + 1
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
		m.Log.Info(s.src.verb()+" ended", "path", s.src.Path)
	}
}

// Sessions counts the sessions, and of them those that transcode video,
// for the admin UI.
func (m *Manager) Sessions() (all, video int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		all++
		if s.src.Video != nil {
			video++
		}
	}
	return all, video
}

// writeConcatList writes the input list of ffmpeg's concat demuxer.
func writeConcatList(path string, files []string) error {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for _, f := range files {
		b.WriteString("file '" + strings.ReplaceAll(f, "'", `'\''`) + "'\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o640)
}

// dirName is the working directory of a session: a hash of its key, which
// holds what clients send (device IDs) and must not become a path.
func dirName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}
