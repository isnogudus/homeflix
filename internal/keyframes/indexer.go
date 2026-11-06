package keyframes

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"homeflix/internal/store"
)

// Indexer finds the keyframes of each media file once and keeps them in
// the database: in the background after a scan (IndexAll), and when a
// file is played before that (Get).
type Indexer struct {
	Store   *store.Store
	Finder  Finder
	Workers int // files indexed at once by IndexAll
	Log     *slog.Logger
	// Shutdown ends the work on a file; finding keyframes goes on when the
	// request that wanted them gives up, so that the next one has them.
	Shutdown context.Context
	Now      func() time.Time

	running atomic.Bool
	mu      sync.Mutex
	calls   map[string]*call // by file ID
}

// call is the finding of the keyframes of one file, shared by everyone
// who wants them meanwhile.
type call struct {
	done   chan struct{}
	times  []time.Duration
	source string
	err    error
}

func (ix *Indexer) now() time.Time {
	if ix.Now != nil {
		return ix.Now()
	}
	return time.Now()
}

func (ix *Indexer) log() *slog.Logger {
	if ix.Log != nil {
		return ix.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Get returns the keyframes of a file: the stored ones if they are
// current, else they are found now and stored. f needs ID, Path, Size,
// ModTime and, for the plausibility check, Duration.
func (ix *Indexer) Get(ctx context.Context, f store.MediaFile) ([]time.Duration, error) {
	k, ok, err := ix.Store.KeyframeIndex(ctx, f.ID, f.Size, f.ModTime)
	if err != nil {
		return nil, err
	}
	if ok {
		if k.Err != "" {
			return nil, errors.New(k.Err)
		}
		return k.Times, nil
	}
	c := ix.start(f)
	select {
	case <-c.done:
		return c.times, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// start finds the keyframes of f unless that is under way already.
func (ix *Indexer) start(f store.MediaFile) *call {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if c, ok := ix.calls[f.ID]; ok {
		return c
	}
	if ix.calls == nil {
		ix.calls = map[string]*call{}
	}
	c := &call{done: make(chan struct{})}
	ix.calls[f.ID] = c
	go func() {
		c.times, c.source, c.err = ix.index(f, true)
		ix.mu.Lock()
		delete(ix.calls, f.ID)
		ix.mu.Unlock()
		close(c.done)
	}()
	return c
}

// index finds and stores the keyframes of f. They are stored under the
// size and modification time the file has now, which the next scan
// records if they differ from f's. Without scan, a file whose container
// index is not usable is left alone and ErrNeedsScan returned.
func (ix *Indexer) index(f store.MediaFile, scan bool) ([]time.Duration, string, error) {
	ctx := ix.Shutdown
	if ctx == nil {
		ctx = context.Background()
	}
	fi, err := os.Stat(f.Path)
	if err != nil {
		return nil, "", err
	}
	finder := ix.Finder
	if !scan {
		finder.FFprobe = ""
	}
	start := ix.now()
	idx, err := finder.Find(ctx, f.Path, f.Duration)
	switch {
	case ctx.Err() != nil:
		return nil, "", ctx.Err()
	case errors.Is(err, fs.ErrNotExist), !scan && errors.Is(err, ErrNeedsScan):
		return nil, "", err
	}
	k := store.KeyframeIndex{FileID: f.ID, Size: fi.Size(), ModTime: fi.ModTime(), Source: idx.Source,
		Times: idx.Times, Note: idx.Note, Took: ix.now().Sub(start), MadeAt: ix.now()}
	log := ix.log().With("path", f.Path, "took", k.Took.Round(time.Millisecond))
	switch {
	case err != nil:
		k.Err = err.Error()
		log.Warn("no keyframes", "err", err)
	case idx.Note != "":
		log.Info("keyframes found", "source", idx.Source, "count", len(idx.Times), "note", idx.Note)
	default:
		log.Debug("keyframes found", "source", idx.Source, "count", len(idx.Times))
	}
	if serr := ix.Store.SetKeyframeIndex(ctx, k); serr != nil {
		log.Error("store keyframes", "err", serr)
	}
	return idx.Times, idx.Source, err
}

// Report counts what IndexAll did.
type Report struct {
	Indexed  int // from the container index
	Scanned  int // with ffprobe
	Failed   int
	Took     time.Duration
	Busy     bool // another IndexAll was running
	Canceled bool
}

// IndexAll finds the keyframes of every file without current ones: first
// from the container indexes, a few files at a time, as they take
// milliseconds; then, one after the other, by scanning the files whose
// index is missing or not believed, which takes seconds to minutes each.
// Only one IndexAll runs at once; a second call returns at once with Busy
// set.
func (ix *Indexer) IndexAll(ctx context.Context) (Report, error) {
	if !ix.running.CompareAndSwap(false, true) {
		return Report{Busy: true}, nil
	}
	defer ix.running.Store(false)
	start := ix.now()
	files, err := ix.Store.UnindexedFiles(ctx)
	if err != nil || len(files) == 0 {
		return Report{}, err
	}
	workers := max(ix.Workers, 1)
	ix.log().Info("finding keyframes", "files", len(files), "workers", workers)
	var rep Report
	var scan []store.MediaFile
	var mu sync.Mutex
	lastLog := start
	count := func(source string, err error) {
		switch {
		case err != nil:
			rep.Failed++
		case source == SourceFFprobe:
			rep.Scanned++
		default:
			rep.Indexed++
		}
		if ix.now().Sub(lastLog) >= 30*time.Second {
			lastLog = ix.now()
			ix.log().Info("finding keyframes", "done", rep.Indexed+rep.Scanned+rep.Failed, "of", len(files),
				"to_scan", len(scan))
		}
	}
	jobs := make(chan store.MediaFile)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				_, source, err := ix.index(f, false)
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				if errors.Is(err, ErrNeedsScan) {
					scan = append(scan, f)
				} else {
					count(source, err)
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, f := range files {
		select {
		case jobs <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if len(scan) > 0 && ctx.Err() == nil {
		ix.log().Info("scanning files without usable keyframe index", "files", len(scan))
	}
	for _, f := range scan {
		if ctx.Err() != nil {
			break
		}
		c := ix.start(f)
		select {
		case <-c.done:
			count(c.source, c.err)
		case <-ctx.Done():
		}
	}
	rep.Took = ix.now().Sub(start)
	rep.Canceled = ctx.Err() != nil
	ix.log().Info("keyframes found", "files", rep.Indexed+rep.Scanned+rep.Failed, "container_index", rep.Indexed,
		"ffprobe", rep.Scanned, "failed", rep.Failed, "took", rep.Took.Round(time.Millisecond))
	return rep, nil
}
