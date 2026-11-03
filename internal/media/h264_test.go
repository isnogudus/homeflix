package media

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Extradata of "12 Years a Slave (2013).mp4" as printed by ffprobe 9;
// ffmpeg's trace_headers reports max_num_ref_frames = 4 for it.
const twelveYearsDump = "\n00000000: 0164 0028 ffe1 001a 6764 0028 acd9 4078  .d.(....gd.(..@x\n" +
	"00000010: 065b 0110 0000 0300 1000 0003 0320 f183  .[........... ..\n" +
	"00000020: 1960 0100 0668 ebe3 cb22 c0fd f8f8 00    .`...h...\"....." + "\n"

func TestParseHexdump(t *testing.T) {
	b, err := ParseHexdump(twelveYearsDump)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 47 || b[0] != 0x01 || b[8] != 0x67 || b[46] != 0x00 {
		t.Errorf("decoded %d bytes: % x", len(b), b)
	}
}

func TestH264RefFramesFromAvcC(t *testing.T) {
	b, err := ParseHexdump(twelveYearsDump)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := H264RefFrames(b)
	if err != nil || refs != 4 {
		t.Errorf("refs = %d, %v; want 4", refs, err)
	}
}

func TestH264RefFramesFromAnnexB(t *testing.T) {
	b, _ := ParseHexdump(twelveYearsDump)
	// The same SPS and PPS with start codes instead of avcC framing.
	sps := b[8 : 8+0x1a]
	annexB := append([]byte{0, 0, 0, 1}, sps...)
	annexB = append(annexB, 0, 0, 0, 1, 0x68, 0xeb, 0xe3, 0xcb, 0x22, 0xc0)
	refs, err := H264RefFrames(annexB)
	if err != nil || refs != 4 {
		t.Errorf("refs = %d, %v; want 4", refs, err)
	}
}

func TestH264RefFramesBaselineProfile(t *testing.T) {
	// Baseline SPS (profile 66, no chroma fields): profile, constraints,
	// level, then ue(0) sps_id, ue(0) log2_max_frame_num, ue(0) poc type,
	// ue(0) log2_max_poc_lsb, ue(2) max_num_ref_frames.
	// Bits: 1 1 1 1 011 → 0b11110110 = 0xf6, padded.
	sps := []byte{66, 0xc0, 30, 0xf6, 0x00}
	refs, err := H264RefFrames(append([]byte{0, 0, 1, 0x67}, sps...))
	if err != nil || refs != 2 {
		t.Errorf("refs = %d, %v; want 2", refs, err)
	}
}

func TestH264RefFramesRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "01", "0164002800e1", "000001670000"} {
		b, _ := hex.DecodeString(in)
		if _, err := H264RefFrames(b); err == nil {
			t.Errorf("%q: want error", in)
		}
	}
	if _, err := ParseHexdump("00000000: zz"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("bad hexdump: %v", err)
	}
}
