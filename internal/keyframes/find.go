package keyframes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MaxGap is the longest stretch without a keyframe that an index may have,
// at the start, between keyframes and before the end, to be believed.
// Feature films have a keyframe every 10 seconds at most, a few every 30.
const MaxGap = time.Minute

// minAverageGap is the shortest average distance between keyframes that
// is believed: a tenth of a second, a few frames.
const minAverageGap = 100 * time.Millisecond

// Check tells whether a keyframe list looks like that of a whole file of
// the given duration; a duration of 0 skips the checks against the end.
// Indexes that point nowhere or cover part of the file fail it.
func Check(times []time.Duration, duration time.Duration) error {
	if len(times) == 0 {
		return errors.New("no keyframes")
	}
	first, last := times[0], times[len(times)-1]
	switch {
	case first < -time.Second:
		return fmt.Errorf("keyframe at %v, before the start", round(first))
	case first > MaxGap:
		return fmt.Errorf("first keyframe at %v", round(first))
	}
	// Some muxers flag every frame of VC-1 as a keyframe, in the blocks
	// and the Cues alike; real video has a keyframe every few frames at
	// most.
	if span := last - first; span > time.Minute && span/time.Duration(len(times)-1) < minAverageGap {
		return fmt.Errorf("a keyframe every %v on average: every frame is flagged",
			round(span/time.Duration(len(times)-1)))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i] - times[i-1]; gap > MaxGap {
			return fmt.Errorf("no keyframe for %v after %v", round(gap), round(times[i-1]))
		}
	}
	if duration > 0 {
		switch {
		case last > duration+5*time.Second:
			return fmt.Errorf("keyframe at %v, after the end at %v", round(last), round(duration))
		case duration-last > MaxGap:
			return fmt.Errorf("last keyframe at %v, %v before the end", round(last), round(duration-last))
		}
	}
	return nil
}

func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// ErrNeedsScan is returned by a Finder without ffprobe for a file whose
// container index is missing or not believed.
var ErrNeedsScan = errors.New("the packets need a scan")

// Index is the keyframe list of a file.
type Index struct {
	Times  []time.Duration
	Source string // SourceMatroska, SourceMP4 or SourceFFprobe
	// Note says why the container index was not used, or what is odd
	// about the keyframes found; empty when all is well.
	Note string
}

// Finder finds keyframes from the container index and falls back to
// scanning the packets with ffprobe.
type Finder struct {
	FFprobe string        // binary; no fallback if empty
	Timeout time.Duration // for the scan of one file; 0 means 30 minutes
	// Scans, if set, bounds the scans running at once to its capacity.
	// Scans read whole files; several at once read slower in total than
	// one after the other.
	Scans chan struct{}
}

// Find returns the keyframes of a file of the given duration (0 if not
// known). The container index is used when it passes Check; otherwise
// ffprobe scans the packets, which reads the whole file: seconds for a
// small file, minutes for a large one. The scan's result is taken even if
// it fails Check, since it is what the file holds; Note says why.
func (f Finder) Find(ctx context.Context, path string, duration time.Duration) (Index, error) {
	times, source, err := readIndex(path)
	var note, rejected string
	switch {
	case err == nil:
		cerr := Check(times, duration)
		if cerr == nil {
			return Index{Times: times, Source: source}, nil
		}
		rejected = cerr.Error()
		note = fmt.Sprintf("%s index rejected: %v", source, cerr)
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission):
		return Index{}, err
	case errors.Is(err, ErrNoIndex):
		note = err.Error()
	default:
		note = fmt.Sprintf("%s index unreadable: %v", source, err)
	}
	if f.FFprobe == "" {
		return Index{}, fmt.Errorf("%w: %s", ErrNeedsScan, note)
	}
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	if f.Scans != nil {
		select {
		case f.Scans <- struct{}{}:
			defer func() { <-f.Scans }()
		case <-ctx.Done():
			return Index{}, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	times, err = Scan(ctx, f.FFprobe, path)
	if err != nil {
		return Index{}, fmt.Errorf("%s; %w", note, err)
	}
	switch cerr := Check(times, duration); {
	case cerr != nil && cerr.Error() == rejected:
		// The file itself ends early, or its duration is too long.
		note += "; the packets agree"
	case cerr != nil:
		note += "; packets: " + cerr.Error()
	}
	return Index{Times: times, Source: SourceFFprobe, Note: note}, nil
}

// Scan lists the keyframes of the first video stream by reading all its
// packets with ffprobe.
func Scan(ctx context.Context, ffprobe, path string) ([]time.Duration, error) {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-hide_banner", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,dts_time,flags", "-of", "compact=p=0", "-i", "file:"+path)
	stderr := &limitedBuffer{max: 2000}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var times []time.Duration
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if t, ok := keyPacket(sc.Text()); ok {
			times = append(times, t)
		}
	}
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("ffprobe scan: %w", ctx.Err())
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffprobe scan: %s", msg)
	case len(times) == 0:
		return nil, errors.New("ffprobe scan: no video keyframes")
	}
	return sortUnique(times), nil
}

// keyPacket parses a line such as "pts_time=1.5|dts_time=1.4|flags=K__"
// and returns its time if it is a keyframe. AVI has no presentation
// times; the decode time stands in.
func keyPacket(line string) (time.Duration, bool) {
	var pts, dts, flags string
	for _, kv := range strings.Split(line, "|") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "pts_time":
			pts = v
		case "dts_time":
			dts = v
		case "flags":
			flags = v
		}
	}
	if !strings.HasPrefix(flags, "K") {
		return 0, false
	}
	v := pts
	if v == "" || v == "N/A" {
		v = dts
	}
	sec, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(sec * float64(time.Second)).Round(time.Microsecond), true
}

// limitedBuffer keeps the first bytes written to it: a damaged file can
// make ffprobe complain for every packet.
type limitedBuffer struct {
	max int
	b   []byte
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b) }
