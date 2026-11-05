package keyframes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

// box is an MP4 box: its type and where its payload starts and ends.
type box struct {
	kind      string
	data, end int64
}

// box reads the header of the box at off, within the bounds end.
func (rd reader) box(off, end int64) (box, error) {
	h, err := rd.bytes(off, 8)
	if err != nil {
		return box{}, err
	}
	size, kind, data := int64(beUint(h[:4])), string(h[4:8]), off+8
	switch size {
	case 0: // to the end of the file
		size = end - off
	case 1:
		l, err := rd.bytes(off+8, 8)
		if err != nil {
			return box{}, err
		}
		size, data = int64(beUint(l)), off+16
	}
	if size < data-off || off+size > end {
		return box{}, fmt.Errorf("invalid %q box", kind)
	}
	return box{kind: kind, data: data, end: off + size}, nil
}

// find returns the first child box of the given kind between data and end.
func (rd reader) find(data, end int64, kind string) (box, bool, error) {
	for off := data; off+8 <= end; {
		b, err := rd.box(off, end)
		if err != nil {
			return box{}, false, err
		}
		if b.kind == kind {
			return b, true, nil
		}
		off = b.end
	}
	return box{}, false, nil
}

// path follows a path of box kinds from the payload of parent.
func (rd reader) path(parent box, kinds ...string) (box, bool, error) {
	b := parent
	for _, k := range kinds {
		next, ok, err := rd.find(b.data, b.end, k)
		if err != nil || !ok {
			return box{}, false, err
		}
		b = next
	}
	return b, true, nil
}

func (rd reader) payload(b box) ([]byte, error) {
	if b.end-b.data > maxIndexBytes {
		return nil, errors.New("box too large")
	}
	return rd.bytes(b.data, int(b.end-b.data))
}

// mp4 reads the sync samples of the first video track.
func mp4(r io.ReaderAt, size int64) ([]time.Duration, error) {
	rd := reader{r, size}
	moov, ok, err := rd.find(0, size, "moov")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: no moov box", ErrNoIndex)
	}
	// The moov box holds all tables; read it once.
	raw, err := rd.payload(moov)
	if err != nil {
		return nil, err
	}
	mem := reader{bytes.NewReader(raw), int64(len(raw))}
	root := box{kind: "moov", data: 0, end: int64(len(raw))}
	for off := root.data; off+8 <= root.end; {
		trak, err := mem.box(off, root.end)
		if err != nil {
			return nil, err
		}
		off = trak.end
		if trak.kind != "trak" {
			continue
		}
		hdlr, ok, err := mem.path(trak, "mdia", "hdlr")
		if err != nil || !ok {
			continue
		}
		h, err := mem.payload(hdlr)
		if err != nil || len(h) < 12 || string(h[8:12]) != "vide" {
			continue
		}
		return videoKeyframes(mem, trak)
	}
	return nil, fmt.Errorf("%w: no video track", ErrNoIndex)
}

func videoKeyframes(mem reader, trak box) ([]time.Duration, error) {
	mdhd, ok, err := mem.path(trak, "mdia", "mdhd")
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: no mdhd box", ErrNoIndex)
	}
	md, err := mem.payload(mdhd)
	if err != nil || len(md) < 24 {
		return nil, fmt.Errorf("%w: short mdhd box", ErrNoIndex)
	}
	var timescale uint64
	if md[0] == 1 {
		if len(md) < 32 {
			return nil, fmt.Errorf("%w: short mdhd box", ErrNoIndex)
		}
		timescale = beUint(md[20:24])
	} else {
		timescale = beUint(md[12:16])
	}
	if timescale == 0 {
		return nil, fmt.Errorf("%w: zero timescale", ErrNoIndex)
	}
	stbl, ok, err := mem.path(trak, "mdia", "minf", "stbl")
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: no stbl box", ErrNoIndex)
	}
	table := func(kind string) ([]byte, bool, error) {
		b, ok, err := mem.find(stbl.data, stbl.end, kind)
		if err != nil || !ok {
			return nil, ok, err
		}
		p, err := mem.payload(b)
		return p, true, err
	}
	stts, ok, err := table("stts")
	if err != nil || !ok || len(stts) < 8 {
		return nil, fmt.Errorf("%w: no stts box", ErrNoIndex)
	}
	stss, hasSync, err := table("stss")
	if err != nil {
		return nil, err
	}
	ctts, _, err := table("ctts")
	if err != nil {
		return nil, err
	}
	// An edit list may start the presentation later than the media.
	var shift int64
	if elst, ok, _ := mem.path(trak, "edts", "elst"); ok {
		if e, err := mem.payload(elst); err == nil && len(e) >= 8 && beUint(e[4:8]) > 0 {
			if e[0] == 1 && len(e) >= 28 {
				shift = int64(beUint(e[16:24]))
			} else if len(e) >= 20 {
				shift = int64(int32(beUint(e[12:16])))
			}
		}
	}

	// Decode times of all samples, from stts runs.
	var dts []int64
	n := int(beUint(stts[4:8]))
	var t int64
	for i := 0; i < n && 8+i*8+8 <= len(stts); i++ {
		count, delta := int(beUint(stts[8+i*8:12+i*8])), int64(beUint(stts[12+i*8:16+i*8]))
		if len(dts)+count > 50_000_000 {
			return nil, errors.New("implausible sample count")
		}
		for range count {
			dts = append(dts, t)
			t += delta
		}
	}
	offset := func(sample int) int64 { return 0 }
	if len(ctts) >= 8 {
		var offs []int64
		version := ctts[0]
		cn := int(beUint(ctts[4:8]))
		for i := 0; i < cn && 8+i*8+8 <= len(ctts) && len(offs) < len(dts); i++ {
			count := int(beUint(ctts[8+i*8 : 12+i*8]))
			v := int64(beUint(ctts[12+i*8 : 16+i*8]))
			if version == 1 {
				v = int64(int32(v))
			}
			for j := 0; j < count && len(offs) < len(dts); j++ {
				offs = append(offs, v)
			}
		}
		offset = func(sample int) int64 {
			if sample < len(offs) {
				return offs[sample]
			}
			return 0
		}
	}
	at := func(sample int) time.Duration {
		pts := max(dts[sample]+offset(sample)-shift, 0)
		return time.Duration(pts) * time.Second / time.Duration(timescale)
	}
	var times []time.Duration
	if !hasSync {
		// Without stss every sample is a sync sample, as in intra-only video;
		// one per second is plenty for segments.
		last := time.Duration(-1)
		for i := range dts {
			if t := at(i); last < 0 || t-last >= time.Second {
				times = append(times, t)
				last = t
			}
		}
		return times, nil
	}
	sn := int(beUint(stss[4:8]))
	for i := 0; i < sn && 8+i*4+4 <= len(stss); i++ {
		sample := int(beUint(stss[8+i*4:12+i*4])) - 1 // numbered from 1
		if sample >= 0 && sample < len(dts) {
			times = append(times, at(sample))
		}
	}
	return times, nil
}
