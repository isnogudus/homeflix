package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/keyframes"
)

// keyframesCommand prints the keyframes of files as the server finds them:
// from the container index if it passes the plausibility check, else by
// scanning the packets with ffprobe. With -index only the container index
// is read, without check. A check for files that do not remux well.
func keyframesCommand(args []string, cio cliIO) error {
	fs := flag.NewFlagSet("keyframes", flag.ContinueOnError)
	fs.SetOutput(cio.stderr)
	all := fs.Bool("all", false, "print every keyframe time")
	indexOnly := fs.Bool("index", false, "read the container index only: no check, no ffprobe")
	ffprobe := fs.String("ffprobe", "ffprobe", "ffprobe binary for the duration and the fallback scan")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: homeflix keyframes [-all] [-index] [-ffprobe path] file...")
	}
	ctx := context.Background()
	failed := 0
	for _, path := range fs.Args() {
		start := time.Now()
		var idx keyframes.Index
		var err error
		if *indexOnly {
			idx.Source = "index"
			idx.Times, err = keyframes.Read(path)
		} else {
			// The duration is not timed: the server has it from probing.
			duration := fileDuration(ctx, *ffprobe, path)
			start = time.Now()
			idx, err = keyframes.Finder{FFprobe: *ffprobe}.Find(ctx, path, duration)
		}
		took := time.Since(start)
		if err != nil {
			fmt.Fprintf(cio.stdout, "%s: %v\n", path, err)
			failed++
			continue
		}
		times := idx.Times
		maxGap := time.Duration(0)
		for i := 1; i < len(times); i++ {
			maxGap = max(maxGap, times[i]-times[i-1])
		}
		avg := time.Duration(0)
		if len(times) > 1 {
			avg = (times[len(times)-1] - times[0]) / time.Duration(len(times)-1)
		}
		fmt.Fprintf(cio.stdout, "%s: %d keyframes from %s, every %v on average, at most %v apart, first %v, last %v (found in %v)\n",
			path, len(times), idx.Source, avg.Round(time.Millisecond), maxGap.Round(time.Millisecond),
			times[0].Round(time.Millisecond), times[len(times)-1].Round(time.Millisecond), took.Round(time.Microsecond))
		if idx.Note != "" {
			fmt.Fprintf(cio.stdout, "  note: %s\n", idx.Note)
		}
		if *all {
			for _, t := range times {
				fmt.Fprintf(cio.stdout, "  %.3f\n", t.Seconds())
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("no keyframes in %d of %d files", failed, fs.NArg())
	}
	return nil
}

// fileDuration asks ffprobe for the duration of a file; 0 if it cannot
// tell, which skips the checks against the end.
func fileDuration(ctx context.Context, ffprobe, path string) time.Duration {
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "csv=p=0", "-i", "file:"+path).Output()
	if err != nil {
		return 0
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return time.Duration(sec * float64(time.Second))
}
