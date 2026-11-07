package media

import (
	"reflect"
	"testing"
	"time"
)

func TestParseAudio(t *testing.T) {
	// An MP3 tagged by Picard (ID3 frames as ffprobe names them), with a
	// cover, and a FLAC with Vorbis comments.
	mp3 := `{"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","sample_rate":"44100","channels":2,
		"channel_layout":"stereo","bit_rate":"259674","disposition":{"default":0}},
		{"index":1,"codec_type":"video","codec_name":"mjpeg","width":500,"height":500,"disposition":{"attached_pic":1}}],
		"format":{"format_name":"mp3","duration":"195.8","bit_rate":"259674","tags":{"title":"I Had a Farm",
		"artist":"John Barry","album_artist":"John Barry","album":"Out of Africa","track":"1/18","disc":"1/1",
		"date":"1997-10-21","genre":"Soundtrack","TSOP":"Barry, John","compilation":"0",
		"MusicBrainz Album Id":"7FD8ECB3-5E01-4A01-B01D-8A359A4524E1","MusicBrainz Artist Id":"661e20c8-3d82-4da2-94a2-97d9e11691c0",
		"MusicBrainz Album Artist Id":"661e20c8-3d82-4da2-94a2-97d9e11691c0","replaygain_track_gain":"-7.77 dB",
		"lyrics-XXX":"  "}}}`
	a, err := ParseAudio(mp3)
	if err != nil {
		t.Fatal(err)
	}
	gain := -7.77
	want := AudioMeta{Title: "I Had a Farm", Artist: "John Barry", ArtistSort: "Barry, John", AlbumArtist: "John Barry",
		Album: "Out of Africa", Track: 1, TrackTotal: 18, Disc: 1, DiscTotal: 1, Date: "1997-10-21",
		Genres: []string{"Soundtrack"}, MBAlbumID: "7fd8ecb3-5e01-4a01-b01d-8a359a4524e1",
		MBAlbumArtistID: "661e20c8-3d82-4da2-94a2-97d9e11691c0", MBArtistIDs: []string{"661e20c8-3d82-4da2-94a2-97d9e11691c0"},
		TrackGain: &gain, Container: "mp3", Codec: "mp3", Channels: 2, ChannelLayout: "stereo", SampleRate: 44100,
		BitRate: 259674, StreamIndex: 0, Cover: 1, CoverCodec: "mjpeg"}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("mp3:\n got %+v\nwant %+v", a, want)
	}
	if a.Year() != 1997 || !a.ReleaseDate().Equal(time.Date(1997, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("year %d, date %v", a.Year(), a.ReleaseDate())
	}

	flac := `{"streams":[{"index":0,"codec_type":"audio","codec_name":"flac","sample_rate":"96000","channels":2,
		"bits_per_raw_sample":"24"}],"format":{"format_name":"flac","duration":"241.0","bit_rate":"952713",
		"tags":{"TITLE":"Gangsta's Paradise","ARTIST":"Coolio; L.V.","ARTISTS":"Coolio;L.V.","ALBUMARTIST":"Coolio",
		"ALBUM":"Gangsta's Paradise","TRACKNUMBER":"03","TRACKTOTAL":"12","DISCNUMBER":"2","DATE":"1995",
		"GENRE":"Hip Hop;Rap","COMPILATION":"1","MUSICBRAINZ_ALBUMID":"1cd1d24c-1705-485c-ae6f-c53e7831b1e4",
		"R128_TRACK_GAIN":"-1792","UNSYNCEDLYRICS":"As I walk through the valley"}}}`
	b, err := ParseAudio(flac)
	if err != nil {
		t.Fatal(err)
	}
	if b.Track != 3 || b.TrackTotal != 12 || b.Disc != 2 || b.Year() != 1995 || !b.Compilation || !b.Lyrics ||
		b.BitDepth != 24 || b.SampleRate != 96000 || b.Cover != -1 || b.AlbumArtist != "Coolio" ||
		!reflect.DeepEqual(b.Artists, []string{"Coolio", "L.V."}) || !reflect.DeepEqual(b.Genres, []string{"Hip Hop", "Rap"}) {
		t.Errorf("flac: %+v", b)
	}
	// R128 -7 dB against -23 LUFS is -2 dB against ReplayGain's -18 LUFS.
	if b.TrackGain == nil || *b.TrackGain != -2 {
		t.Errorf("R128 gain: %v", b.TrackGain)
	}

	info := b.Info(241*time.Second, "en")
	if len(info.Streams) != 1 || info.Streams[0].Codec != "flac" || info.Container != "flac" || info.Duration != 241*time.Second {
		t.Errorf("info: %+v", info)
	}
	if _, err := ParseAudio(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264"}],"format":{}}`); err == nil {
		t.Error("a file without audio parsed")
	}
}

func TestTagKey(t *testing.T) {
	for _, k := range []string{"MusicBrainz Album Id", "MUSICBRAINZ_ALBUMID", "MusicBrainz/Album Id", "musicbrainz-albumid"} {
		if tagKey(k) != "musicbrainzalbumid" {
			t.Errorf("%q -> %q", k, tagKey(k))
		}
	}
}

func TestParseLyrics(t *testing.T) {
	l := ParseLyrics("\ufeff[ar:Evanescence]\n[ti:Sick]\n[offset:+500]\n[00:12.50]I'm so sick\n[00:15.20][01:15.20]Of this\n\n")
	if l.Artist != "Evanescence" || l.Title != "Sick" || len(l.Lines) != 3 {
		t.Fatalf("%+v", l)
	}
	if *l.Lines[0].Start != 12*time.Second || l.Lines[0].Text != "I'm so sick" || *l.Lines[2].Start != 74700*time.Millisecond {
		t.Errorf("times: %v %v", *l.Lines[0].Start, *l.Lines[2].Start)
	}
	plain := ParseLyrics("First line\n\nSecond line\n")
	if len(plain.Lines) != 3 || plain.Lines[0].Start != nil || plain.Lines[2].Text != "Second line" {
		t.Errorf("plain: %+v", plain)
	}
}
