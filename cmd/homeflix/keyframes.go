package main

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"homeflix/internal/keyframes"
)

// keyframesCommand prints the keyframes of files as homeflix reads them
// from the container index, with the time it took: a check for files that
// do not remux well.
func keyframesCommand(args []string, cio cliIO) error {
	fs := flag.NewFlagSet("keyframes", flag.ContinueOnError)
	fs.SetOutput(cio.stderr)
	all := fs.Bool("all", false, "print every keyframe time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: homeflix keyframes [-all] file...")
	}
	failed := 0
	for _, path := range fs.Args() {
		start := time.Now()
		times, err := keyframes.Read(path)
		took := time.Since(start)
		if err != nil {
			fmt.Fprintf(cio.stdout, "%s: %v\n", path, err)
			failed++
			continue
		}
		maxGap := time.Duration(0)
		for i := 1; i < len(times); i++ {
			maxGap = max(maxGap, times[i]-times[i-1])
		}
		avg := time.Duration(0)
		if len(times) > 1 {
			avg = (times[len(times)-1] - times[0]) / time.Duration(len(times)-1)
		}
		fmt.Fprintf(cio.stdout, "%s: %d keyframes, every %v on average, at most %v apart, first %v, last %v (read in %v)\n",
			path, len(times), avg.Round(time.Millisecond), maxGap.Round(time.Millisecond), times[0],
			times[len(times)-1].Round(time.Millisecond), took.Round(time.Microsecond))
		if *all {
			for _, t := range times {
				fmt.Fprintf(cio.stdout, "  %.3f\n", t.Seconds())
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files without usable keyframe index", failed, fs.NArg())
	}
	return nil
}
