// Package media interprets probe data: codec details clients need to
// decide whether they can play a file.
package media

import (
	"encoding/hex"
	"errors"
	"strings"
)

// ParseHexdump decodes the extradata hexdump ffprobe prints with
// -show_data ("00000000: 0164 0028 …  .d.(").
func ParseHexdump(dump string) ([]byte, error) {
	var out []byte
	for _, line := range strings.Split(dump, "\n") {
		_, rest, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		if i := strings.Index(rest, "  "); i >= 0 {
			rest = rest[:i]
		}
		b, err := hex.DecodeString(strings.ReplaceAll(rest, " ", ""))
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// H264RefFrames returns max_num_ref_frames from the sequence parameter set
// in H.264 extradata, either an avcC record (MP4, Matroska) or Annex B
// start-code NAL units (MPEG-TS).
func H264RefFrames(extradata []byte) (int, error) {
	sps, err := findSPS(extradata)
	if err != nil {
		return 0, err
	}
	return spsMaxRefFrames(unescapeRBSP(sps))
}

var errNoSPS = errors.New("no sequence parameter set in extradata")

func findSPS(b []byte) ([]byte, error) {
	if len(b) >= 8 && b[0] == 1 {
		// avcC: version, profile, compat, level, length size, SPS count,
		// then 16-bit length and NAL unit.
		count := int(b[5] & 0x1f)
		pos := 6
		for range count {
			if pos+2 > len(b) {
				break
			}
			n := int(b[pos])<<8 | int(b[pos+1])
			pos += 2
			if pos+n > len(b) || n < 1 {
				break
			}
			if b[pos]&0x1f == 7 {
				return b[pos+1 : pos+n], nil
			}
			pos += n
		}
		return nil, errNoSPS
	}
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 && b[i+3]&0x1f == 7 {
			end := len(b)
			for j := i + 4; j+2 < len(b); j++ {
				if b[j] == 0 && b[j+1] == 0 && (b[j+2] == 1 || b[j+2] == 0) {
					end = j
					break
				}
			}
			return b[i+4 : end], nil
		}
	}
	return nil, errNoSPS
}

// unescapeRBSP removes emulation prevention bytes (00 00 03 → 00 00).
func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type bitReader struct {
	b   []byte
	pos int // bit position
	err error
}

var errShortSPS = errors.New("sequence parameter set too short")

func (r *bitReader) bit() uint {
	if r.pos >= len(r.b)*8 {
		r.err = errShortSPS
		return 0
	}
	v := r.b[r.pos/8] >> (7 - r.pos%8) & 1
	r.pos++
	return uint(v)
}

func (r *bitReader) bits(n int) uint {
	var v uint
	for range n {
		v = v<<1 | r.bit()
	}
	return v
}

// ue reads an unsigned Exp-Golomb code.
func (r *bitReader) ue() uint {
	zeros := 0
	for r.bit() == 0 && r.err == nil {
		zeros++
		if zeros > 31 {
			r.err = errors.New("invalid Exp-Golomb code")
			return 0
		}
	}
	return (1<<zeros - 1) + r.bits(zeros)
}

// se reads a signed Exp-Golomb code.
func (r *bitReader) se() int {
	v := r.ue()
	if v%2 == 1 {
		return int(v+1) / 2
	}
	return -int(v / 2)
}

func (r *bitReader) skipScalingList(size int) {
	last, next := 8, 8
	for range size {
		if next != 0 {
			next = (last + r.se() + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
}

// spsMaxRefFrames parses seq_parameter_set_data (ITU-T H.264 7.3.2.1.1) up
// to max_num_ref_frames.
func spsMaxRefFrames(rbsp []byte) (int, error) {
	r := &bitReader{b: rbsp}
	profile := r.bits(8)
	r.bits(16) // constraint flags, level
	r.ue()     // seq_parameter_set_id
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma := r.ue()
		if chroma == 3 {
			r.bit() // separate_colour_plane_flag
		}
		r.ue()  // bit_depth_luma_minus8
		r.ue()  // bit_depth_chroma_minus8
		r.bit() // qpprime_y_zero_transform_bypass_flag
		if r.bit() == 1 {
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := range lists {
				if r.bit() == 1 {
					if i < 6 {
						r.skipScalingList(16)
					} else {
						r.skipScalingList(64)
					}
				}
			}
		}
	}
	r.ue() // log2_max_frame_num_minus4
	switch r.ue() {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.bit() // delta_pic_order_always_zero_flag
		r.se()  // offset_for_non_ref_pic
		r.se()  // offset_for_top_to_bottom_field
		n := r.ue()
		if n > 255 {
			return 0, errors.New("invalid num_ref_frames_in_pic_order_cnt_cycle")
		}
		for range n {
			r.se()
		}
	}
	refs := r.ue()
	if r.err != nil {
		return 0, r.err
	}
	return int(refs), nil
}
