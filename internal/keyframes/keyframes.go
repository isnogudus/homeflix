// Package keyframes finds the video keyframes of a file from the index of
// its container: the Cues of Matroska and the sync sample table of MP4.
// That takes a few kilobytes of reads, where scanning the packets with
// ffprobe would read the whole file. Copied video can only be cut at
// keyframes, so HLS playlists of remuxed files are built from these times.
package keyframes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// ErrNoIndex is returned when the container has no usable keyframe index.
var ErrNoIndex = errors.New("no keyframe index in the container")

// Read returns the presentation times of the video keyframes of a file,
// in order.
func Read(path string) ([]time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return FromReader(f, fi.Size())
}

// FromReader is Read for an open file of the given size.
func FromReader(r io.ReaderAt, size int64) ([]time.Duration, error) {
	head := make([]byte, 12)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	var times []time.Duration
	var err error
	switch {
	case bytes.Equal(head[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		times, err = matroska(r, size)
	case string(head[4:8]) == "ftyp":
		times, err = mp4(r, size)
	default:
		return nil, fmt.Errorf("%w: unknown container", ErrNoIndex)
	}
	if err != nil {
		return nil, err
	}
	if len(times) == 0 {
		return nil, ErrNoIndex
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	out := times[:1]
	for _, t := range times[1:] {
		if t != out[len(out)-1] {
			out = append(out, t)
		}
	}
	return out, nil
}

// reader reads big-endian values at offsets of a file.
type reader struct {
	r    io.ReaderAt
	size int64
}

func (rd reader) bytes(off int64, n int) ([]byte, error) {
	if n < 0 || off < 0 || off+int64(n) > rd.size {
		return nil, io.ErrUnexpectedEOF
	}
	b := make([]byte, n)
	if _, err := rd.r.ReadAt(b, off); err != nil && !(errors.Is(err, io.EOF) && off+int64(n) == rd.size) {
		return nil, err
	}
	return b, nil
}

func beUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// maxIndexBytes bounds how much of a file an index may take up: Cues and
// sample tables of feature films are a few megabytes at most.
const maxIndexBytes = 64 << 20
