package keyframes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

// Matroska element IDs, with their length marker as written.
const (
	idEBML          = 0x1A45DFA3
	idSegment       = 0x18538067
	idSeekHead      = 0x114D9B74
	idSeek          = 0x4DBB
	idSeekID        = 0x53AB
	idSeekPosition  = 0x53AC
	idInfo          = 0x1549A966
	idTimecodeScale = 0x2AD7B1
	idTracks        = 0x1654AE6B
	idTrackEntry    = 0xAE
	idTrackNumber   = 0xD7
	idTrackType     = 0x83
	idCues          = 0x1C53BB6B
	idCuePoint      = 0xBB
	idCueTime       = 0xB3
	idCuePositions  = 0xB7
	idCueTrack      = 0xF7
	idCueCluster    = 0xF1
	idCueRelative   = 0xF0
	idCluster       = 0x1F43B675
	idSimpleBlock   = 0xA3
	idBlockGroup    = 0xA0
	idBlock         = 0xA1
	idRefBlock      = 0xFB
)

// unknownSize marks an element whose size is not written (live streams).
const unknownSize = -1

// element is a Matroska element: where it and its data start, and how long
// the data is.
type element struct {
	id                uint64
	start, data, size int64
}

// vint reads a variable-length integer at off. With keepMarker, the
// length marker stays in the value, as in element IDs.
func (rd reader) vint(off int64, keepMarker bool) (uint64, int, error) {
	first, err := rd.bytes(off, 1)
	if err != nil {
		return 0, 0, err
	}
	n := 1
	for mask := byte(0x80); n <= 8 && first[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 {
		return 0, 0, errors.New("invalid EBML integer")
	}
	b, err := rd.bytes(off, n)
	if err != nil {
		return 0, 0, err
	}
	if !keepMarker {
		b[0] &= 0xFF >> n
	}
	return beUint(b), n, nil
}

// element reads the header of the element at off.
func (rd reader) element(off int64) (element, error) {
	id, n, err := rd.vint(off, true)
	if err != nil {
		return element{}, err
	}
	size, m, err := rd.vint(off+int64(n), false)
	if err != nil {
		return element{}, err
	}
	e := element{id: id, start: off, data: off + int64(n+m), size: int64(size)}
	if size == 1<<(7*m)-1 {
		e.size = unknownSize
	}
	return e, nil
}

// children calls fn for the child elements of e; fn returns false to stop.
func (rd reader) children(e element, fn func(element) (bool, error)) error {
	end := e.data + e.size
	if e.size == unknownSize {
		end = rd.size
	}
	for off := e.data; off < end; {
		c, err := rd.element(off)
		if err != nil {
			return err
		}
		more, err := fn(c)
		if err != nil || !more {
			return err
		}
		if c.size == unknownSize {
			return nil
		}
		off = c.data + c.size
	}
	return nil
}

func (rd reader) uint(e element) (uint64, error) {
	if e.size < 1 || e.size > 8 {
		return 0, errors.New("invalid EBML unsigned integer")
	}
	b, err := rd.bytes(e.data, int(e.size))
	return beUint(b), err
}

// matroska reads the Cue times of the first video track.
func matroska(r io.ReaderAt, size int64) ([]time.Duration, error) {
	rd := reader{r, size}
	head, err := rd.element(0)
	if err != nil || head.id != idEBML {
		return nil, fmt.Errorf("%w: not an EBML file", ErrNoIndex)
	}
	seg, err := rd.element(head.data + head.size)
	if err != nil || seg.id != idSegment {
		return nil, fmt.Errorf("%w: no Matroska segment", ErrNoIndex)
	}
	scale := uint64(1000000) // TimecodeScale default: milliseconds
	videoTrack := uint64(0)
	var cues, info, tracks int64 = -1, -1, -1
	// The top-level elements up to the first cluster, and what the SeekHead
	// points to beyond it, usually the Cues at the end.
	err = rd.children(seg, func(e element) (bool, error) {
		switch e.id {
		case idSeekHead:
			return true, rd.children(e, func(s element) (bool, error) {
				if s.id != idSeek {
					return true, nil
				}
				var id uint64
				pos := int64(-1)
				err := rd.children(s, func(f element) (bool, error) {
					switch f.id {
					case idSeekID:
						b, err := rd.bytes(f.data, int(f.size))
						id = beUint(b)
						return true, err
					case idSeekPosition:
						p, err := rd.uint(f)
						pos = seg.data + int64(p)
						return true, err
					}
					return true, nil
				})
				switch {
				case id == idCues && cues < 0:
					cues = pos
				case id == idInfo && info < 0:
					info = pos
				case id == idTracks && tracks < 0:
					tracks = pos
				}
				return true, err
			})
		case idInfo:
			info = e.start
		case idTracks:
			tracks = e.start
		case idCues:
			cues = e.start
		case idCluster:
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if info >= 0 {
		if e, err := rd.element(info); err == nil && e.id == idInfo {
			rd.children(e, func(c element) (bool, error) {
				if c.id == idTimecodeScale {
					if v, err := rd.uint(c); err == nil && v > 0 {
						scale = v
					}
				}
				return true, nil
			})
		}
	}
	if tracks >= 0 {
		if e, err := rd.element(tracks); err == nil && e.id == idTracks {
			rd.children(e, func(t element) (bool, error) {
				if t.id != idTrackEntry {
					return true, nil
				}
				var number, kind uint64
				rd.children(t, func(c element) (bool, error) {
					switch c.id {
					case idTrackNumber:
						number, _ = rd.uint(c)
					case idTrackType:
						kind, _ = rd.uint(c)
					}
					return true, nil
				})
				if kind == 1 { // video
					videoTrack = number
					return false, nil
				}
				return true, nil
			})
		}
	}
	if cues < 0 {
		return nil, fmt.Errorf("%w: no Cues", ErrNoIndex)
	}
	ce, err := rd.element(cues)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: Cues at %d: %v", ErrNoIndex, cues, err)
	case ce.id != idCues || ce.size == unknownSize || ce.size > maxIndexBytes:
		return nil, fmt.Errorf("%w: element %#x of size %d at %d where the Cues should be", ErrNoIndex, ce.id, ce.size, cues)
	}
	// Read the Cues at once rather than element by element.
	raw, err := rd.bytes(cues, int(ce.data-cues+ce.size))
	if err != nil {
		return nil, err
	}
	mem := reader{bytes.NewReader(raw), int64(len(raw))}
	ce.data -= cues
	var times []time.Duration
	var blocks []cueBlock
	err = mem.children(ce, func(p element) (bool, error) {
		if p.id != idCuePoint {
			return true, nil
		}
		var t uint64
		forVideo := videoTrack == 0
		block := cueBlock{cluster: -1}
		err := mem.children(p, func(c element) (bool, error) {
			switch c.id {
			case idCueTime:
				v, err := mem.uint(c)
				t = v
				return true, err
			case idCuePositions:
				var track uint64
				cluster, relative := int64(-1), int64(-1)
				err := mem.children(c, func(tp element) (bool, error) {
					switch tp.id {
					case idCueTrack:
						track, _ = mem.uint(tp)
					case idCueCluster:
						v, err := mem.uint(tp)
						cluster = int64(v)
						return true, err
					case idCueRelative:
						v, err := mem.uint(tp)
						relative = int64(v)
						return true, err
					}
					return true, nil
				})
				if videoTrack == 0 || track == videoTrack {
					forVideo = true
					if cluster >= 0 && relative >= 0 {
						block = cueBlock{cluster: seg.data + cluster, relative: relative}
					}
				}
				return true, err
			}
			return true, nil
		})
		if forVideo {
			times = append(times, time.Duration(t*scale))
			blocks = append(blocks, block)
		}
		return true, err
	})
	if err != nil {
		return nil, err
	}
	if err := rd.checkBlocks(blocks, videoTrack); err != nil {
		return nil, err
	}
	return times, nil
}

// cueBlock is where a cue point says its block is: the file offset of the
// cluster, and the position of the block in the cluster's data.
type cueBlock struct{ cluster, relative int64 }

// cueSamples is how many cue points are checked against their blocks.
const cueSamples = 12

// checkBlocks reads the blocks of a few cue points spread over the file
// and makes sure they are keyframes of the video track. Some muxers cue
// every frame, and Cues of a damaged file point anywhere; neither can be
// used to cut. Cue points without a relative position are not checked.
func (rd reader) checkBlocks(blocks []cueBlock, videoTrack uint64) error {
	for i := range cueSamples {
		if len(blocks) == 0 {
			break
		}
		b := blocks[i*(len(blocks)-1)/max(cueSamples-1, 1)]
		if b.cluster < 0 {
			continue
		}
		cl, err := rd.element(b.cluster)
		if err != nil || cl.id != idCluster {
			return fmt.Errorf("cue point at %d: no cluster there", b.cluster)
		}
		off := cl.data + b.relative
		key, track, err := rd.block(off)
		switch {
		case err != nil:
			return fmt.Errorf("cue point at %d: %w", off, err)
		case videoTrack != 0 && track != videoTrack:
			return fmt.Errorf("cue point at %d is a block of track %d, not of video track %d", off, track, videoTrack)
		case !key:
			return fmt.Errorf("cue point at %d is not a keyframe: the Cues list other frames too", off)
		}
	}
	return nil
}

// block reads whether the SimpleBlock or BlockGroup at off is a keyframe,
// and its track.
func (rd reader) block(off int64) (key bool, track uint64, err error) {
	e, err := rd.element(off)
	if err != nil {
		return false, 0, err
	}
	header := func(data int64) (uint64, byte, error) {
		track, n, err := rd.vint(data, false)
		if err != nil {
			return 0, 0, err
		}
		flags, err := rd.bytes(data+int64(n)+2, 1) // after the 16-bit timecode
		if err != nil {
			return 0, 0, err
		}
		return track, flags[0], nil
	}
	switch e.id {
	case idSimpleBlock:
		track, flags, err := header(e.data)
		return flags&0x80 != 0, track, err
	case idBlockGroup:
		// A Block in a group is a keyframe unless it references another.
		key, found := true, false
		err := rd.children(e, func(c element) (bool, error) {
			switch c.id {
			case idBlock:
				found = true
				track, _, err = header(c.data)
				return true, err
			case idRefBlock:
				key = false
			}
			return true, nil
		})
		if err == nil && !found {
			err = errors.New("block group without block")
		}
		return key, track, err
	}
	return false, 0, fmt.Errorf("element %#x where a block should be", e.id)
}
