package library

import (
	"context"
	"encoding/json"
	"testing"

	"homeflix/internal/store"
)

type jsonProber map[string]string // file base name -> probe JSON

func (p jsonProber) Probe(ctx context.Context, path string) store.ProbeResult {
	for name, js := range p {
		if len(path) >= len(name) && path[len(path)-len(name):] == name {
			return store.ProbeResult{JSON: js}
		}
	}
	return store.ProbeResult{Err: "unknown file"}
}

func TestCollectStats(t *testing.T) {
	f := newScanFixture(t)
	extradata, _ := json.Marshal("\n00000000: 0164 0028 ffe1 001a 6764 0028 acd9 4078  .d.(....gd.(..@x\n" +
		"00000010: 065b 0110 0000 0300 1000 0003 0320 f183  .[........... ..\n" +
		"00000020: 1960 0100 0668 ebe3 cb22 c0fd f8f8 00    .`...h...\".....\n")
	f.sc.Prober = jsonProber{
		"A (2001).mp4": `{"streams":[
			{"codec_type":"video","codec_name":"h264","profile":"High","width":1920,"height":1080,"extradata":` + string(extradata) + `},
			{"codec_type":"audio","codec_name":"ac3"},
			{"codec_type":"audio","codec_name":"dts","profile":"DTS-HD MA"},
			{"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle"}]}`,
		"B.mkv": `{"streams":[
			{"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}},
			{"codec_type":"video","codec_name":"hevc","profile":"Main 10","width":3840,"height":2160,"color_transfer":"smpte2084"},
			{"codec_type":"audio","codec_name":"truehd"}]}`,
	}
	f.write("A (2001).mp4", "a")
	f.write("B.mkv", "b")
	f.write("Broken (2003).avi", "c")
	f.scan()

	s, err := CollectStats(context.Background(), f.store)
	if err != nil {
		t.Fatal(err)
	}
	if s.Movies != 3 || s.Files != 3 || len(s.Failed) != 1 || s.Unprobed != 0 {
		t.Errorf("counts: %+v", s)
	}
	if s.VideoCodecs["h264 High"] != 1 || s.VideoCodecs["hevc Main 10"] != 1 || s.VideoCodecs["mjpeg"] != 0 {
		t.Errorf("video: %v", s.VideoCodecs)
	}
	if s.Resolutions["1080p"] != 1 || s.Resolutions["2160p"] != 1 || s.HDR["HDR10"] != 1 {
		t.Errorf("resolution %v, hdr %v", s.Resolutions, s.HDR)
	}
	if s.AudioCodecs["dts DTS-HD MA"] != 1 || s.AudioTracks["2"] != 1 || s.SubtitleCodecs["hdmv_pgs_subtitle"] != 1 {
		t.Errorf("audio %v, tracks %v, subs %v", s.AudioCodecs, s.AudioTracks, s.SubtitleCodecs)
	}
	if s.H264ManyRefFrames != 0 || s.H264UnknownRefFrames != 0 || s.H264RefFrames["4"] != 1 {
		t.Errorf("ref frames: many %d, unknown %d", s.H264ManyRefFrames, s.H264UnknownRefFrames)
	}
	if len(s.NoYear) != 1 || s.NoYear[0] != "B" {
		t.Errorf("no year: %v", s.NoYear)
	}
}
