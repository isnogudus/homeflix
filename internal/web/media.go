package web

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/hls"
	"homeflix/internal/media"
	"homeflix/internal/playstate"
	"homeflix/internal/store"
	"homeflix/internal/subtitles"
)

// playability says whether a browser can play a file as it is, and if not
// why: a text key and the codec or container in the way.
type playability struct {
	OK     bool
	Reason string // "needsRemux", "needsAudio", "needsVideo", "conversionOff", "transcodeOff"
	Detail string
}

// browserAudio are the audio codecs every current browser decodes.
var browserAudio = map[string]bool{"aac": true, "mp3": true, "opus": true, "flac": true, "vorbis": true}

// directPlay decides whether browsers can play a part as it is. It is
// strict: what passes plays in Safari, Chrome, Firefox and Edge. Other
// files go through HLS: remuxed, or with their video transcoded.
func directPlay(p catalog.Part) playability {
	ext := strings.ToLower(filepath.Ext(p.File.Path))
	v, hasVideo := p.Info.Video()
	switch {
	case ext != ".mp4" && ext != ".m4v" && ext != ".mov" && ext != ".webm":
		return playability{Reason: "needsRemux", Detail: strings.TrimPrefix(ext, ".")}
	case !hasVideo:
		return playability{Reason: "needsVideo", Detail: "?"}
	case !(v.Codec == "h264" && v.BitDepth <= 8) && !(v.Codec == "vp9" && ext == ".webm"):
		detail := v.Codec
		if v.Profile != "" {
			detail += " " + v.Profile
		}
		return playability{Reason: "needsVideo", Detail: detail}
	}
	if a := defaultAudio(p.Info); a != nil && !browserAudio[strings.ToLower(a.Codec)] {
		return playability{Reason: "needsAudio", Detail: strings.ToUpper(a.Codec)}
	}
	return playability{OK: true}
}

// remuxable reports whether the server can make a part playable by
// copying its video into HLS and converting the audio: h264 in 8 bits, in
// any container. Other video is transcoded.
func remuxable(p catalog.Part) playability {
	v, ok := p.Info.Video()
	switch {
	case !ok:
		return playability{Reason: "needsVideo", Detail: "?"}
	case v.Codec != "h264" || v.BitDepth > 8:
		detail := v.Codec
		if v.Profile != "" {
			detail += " " + v.Profile
		}
		return playability{Reason: "needsVideo", Detail: detail}
	}
	return playability{OK: true}
}

// playable says how a part plays in browsers for a user: as it is
// (direct), remuxed by the server (hls), with its video transcoded
// (transcode), or not, because the user's account does not allow the
// conversion it needs.
func (s *Server) playable(p catalog.Part, conv store.Conversion) (mode string, why playability) {
	d := directPlay(p)
	r := remuxable(p)
	_, hasVideo := p.Info.Video()
	switch {
	case d.OK:
		return "direct", d
	case !hasVideo:
		return "", r
	case s.HLS == nil && !r.OK:
		return "", r
	case s.HLS == nil:
		return "", d
	case !conv.Remux():
		return "", playability{Reason: "conversionOff", Detail: d.Detail}
	case r.OK:
		return "hls", d
	case !conv.Transcode():
		return "", playability{Reason: "transcodeOff", Detail: r.Detail}
	}
	return "transcode", r
}

// hlsFor reports whether the server converts parts for a user at all,
// and whether it may transcode video for them.
func (s *Server) hlsFor(conv store.Conversion) (remux, transcode bool) {
	if s.HLS == nil {
		return false, false
	}
	return conv.Remux(), conv.Transcode()
}

// hlsSource describes the conversion of one audio track of a part, with
// the video transcoded as v says or, if v is nil, copied.
func hlsSource(p catalog.Part, audio int, v *hls.Video) hls.Source {
	return hls.Source{Key: fmt.Sprintf("%s-%d-%d", p.File.ID, p.File.Size, p.File.ModTime.UnixMilli()),
		FileID: p.File.ID, Path: p.File.Path, Size: p.File.Size, ModTime: p.File.ModTime,
		Duration: p.Info.Duration, Audio: audio, Video: v}
}

// transcodeVideo says how the video of a part is transcoded: scaled to fit
// 1080p, deinterlaced and mapped from HDR to SDR as needed, with the image
// subtitle sub burnt in (-1 for none).
func transcodeVideo(p catalog.Part, sub int) *hls.Video {
	v, _ := p.Info.Video()
	w, h := hls.FitSize(v.Width, v.Height)
	return &hls.Video{Width: w, Height: h, Deinterlace: v.IsInterlaced, ToneMap: v.VideoRange == "HDR", Subtitle: sub}
}

// parseVariant reads the variant of an HLS URL: "<audio>" remuxes with
// that audio track, "<audio>t" transcodes the video, and "<audio>t<sub>"
// burns the image subtitle with stream index sub in as well.
func parseVariant(s string) (audio int, transcode bool, sub int, ok bool) {
	a, t, transcode := strings.Cut(s, "t")
	audio, err := strconv.Atoi(a)
	if err != nil || audio < 0 || a != strconv.Itoa(audio) {
		return 0, false, 0, false
	}
	sub = -1
	if t != "" {
		if sub, err = strconv.Atoi(t); err != nil || sub < 0 || t != strconv.Itoa(sub) {
			return 0, false, 0, false
		}
	}
	return audio, transcode, sub, true
}

// hlsPart finds the part and variant of an HLS request.
func (s *Server) hlsPart(w http.ResponseWriter, r *http.Request, sess *session) (catalog.PartRef, hls.Source, bool) {
	if s.Catalog == nil || s.HLS == nil {
		http.NotFound(w, r)
		return catalog.PartRef{}, hls.Source{}, false
	}
	remux, mayTranscode := s.hlsFor(sess.User.Conversion)
	if !remux {
		http.Error(w, "this account plays movies only as they are", http.StatusForbidden)
		return catalog.PartRef{}, hls.Source{}, false
	}
	ref, ok := s.Catalog.Snapshot().Part(r.PathValue("file"))
	audio, transcode, sub, valid := parseVariant(r.PathValue("variant"))
	if !ok || !valid || !hasStream(ref.Part().Info, audio, "Audio") ||
		!transcode && !remuxable(ref.Part()).OK || sub >= 0 && !hasImageSubtitle(ref.Part().Info, sub) {
		http.NotFound(w, r)
		return catalog.PartRef{}, hls.Source{}, false
	}
	if !transcode {
		src := hlsSource(ref.Part(), audio, nil)
		src.Owner = sess.Token.SessionID
		return ref, src, true
	}
	if _, hasVideo := ref.Part().Info.Video(); !hasVideo {
		http.NotFound(w, r)
		return catalog.PartRef{}, hls.Source{}, false
	}
	if !mayTranscode {
		http.Error(w, "this account may not have video transcoded", http.StatusForbidden)
		return catalog.PartRef{}, hls.Source{}, false
	}
	src := hlsSource(ref.Part(), audio, transcodeVideo(ref.Part(), sub))
	src.Owner = sess.Token.SessionID
	return ref, src, true
}

func hasStream(info media.Info, index int, kind string) bool {
	for _, st := range info.Streams {
		if st.Index == index && st.Type == kind {
			return true
		}
	}
	return false
}

func hasImageSubtitle(info media.Info, index int) bool {
	for _, st := range info.Streams {
		if st.Index == index && st.Type == "Subtitle" {
			return !st.IsTextSubtitleStream
		}
	}
	return false
}

// hlsPlaylist serves the playlist of a remuxed part.
func (s *Server) hlsPlaylist(w http.ResponseWriter, r *http.Request, sess *session) {
	ref, src, ok := s.hlsPart(w, r, sess)
	if !ok {
		return
	}
	segs, err := s.HLS.Segments(r.Context(), src)
	if err != nil {
		s.Log.Warn("no segment plan", "item", ref.Movie.Title, "err", err)
		http.Error(w, "this file cannot be converted", http.StatusUnprocessableEntity)
		return
	}
	kind := "remux"
	if src.Video != nil {
		kind = "transcode"
	}
	s.Log.Info(kind+" playlist", "item", ref.Movie.Title, "part", ref.Index+1, "audio", src.Audio, "user", sess.User.Name)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(hls.Playlist(segs, func(i int) string { return strconv.Itoa(i) + ".ts" }))
}

// hlsSegment serves a segment, waiting for ffmpeg to make it.
func (s *Server) hlsSegment(w http.ResponseWriter, r *http.Request, sess *session) {
	_, src, ok := s.hlsPart(w, r, sess)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("segment"), ".ts"))
	if err != nil || !strings.HasSuffix(r.PathValue("segment"), ".ts") {
		http.NotFound(w, r)
		return
	}
	path, err := s.HLS.Segment(r.Context(), src, n)
	switch {
	case errors.Is(err, hls.ErrBusy):
		w.Header().Set("Retry-After", "10")
		http.Error(w, "the server is busy converting other movies", http.StatusServiceUnavailable)
		return
	case err != nil:
		if r.Context().Err() == nil {
			s.Log.Warn("segment failed", "path", src.Path, "segment", n, "err", err)
			http.Error(w, "segment not available", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

// defaultAudio is the audio track a browser plays: the one marked default,
// else the first.
func defaultAudio(info media.Info) *media.Stream {
	idx := info.DefaultAudioIndex()
	for i := range info.Streams {
		if info.Streams[i].Index == idx {
			return &info.Streams[i]
		}
	}
	return nil
}

// contentTypes of the containers browsers play directly.
var contentTypes = map[string]string{".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".m4b": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac",
	".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav"}

// stream serves a file of a movie for the player, with ranges for seeking.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, sess *session) {
	s.serveFile(w, r, sess, false)
}

// download serves a file of a movie as an attachment, to users with the
// right to download.
func (s *Server) download(w http.ResponseWriter, r *http.Request, sess *session) {
	if !sess.User.CanDownload {
		http.Error(w, "this account may not download files", http.StatusForbidden)
		return
	}
	s.serveFile(w, r, sess, true)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, sess *session, attachment bool) {
	if s.Catalog == nil {
		http.NotFound(w, r)
		return
	}
	ref, ok := s.Catalog.Snapshot().Part(r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := ref.Part().File.Path
	f, err := os.Open(path)
	if err != nil {
		s.Log.Error("open media file", "path", path, "err", err)
		http.Error(w, "media file not available", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.fail(w, err)
		return
	}
	ct := contentTypes[strings.ToLower(filepath.Ext(path))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	switch {
	case attachment:
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
		if r.Header.Get("Range") == "" {
			s.Log.Info("download", "item", ref.Movie.Title, "part", ref.Index+1, "user", sess.User.Name, "client", clientName)
		}
	case r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-"):
		s.Log.Info("stream", "item", ref.Movie.Title, "part", ref.Index+1, "user", sess.User.Name, "client", clientName)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// subtitleTrack serves a text subtitle track of a file as WebVTT.
func (s *Server) subtitleTrack(w http.ResponseWriter, r *http.Request, sess *session) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || s.Catalog == nil || s.Subtitles == nil {
		http.NotFound(w, r)
		return
	}
	ref, ok := s.Catalog.Snapshot().Part(r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	path, err := s.Subtitles.Get(r.Context(), ref.Part(), index, "vtt")
	switch {
	case errors.Is(err, subtitles.ErrNotText):
		http.NotFound(w, r)
		return
	case err != nil:
		if r.Context().Err() == nil {
			s.Log.Warn("subtitles unavailable", "item", ref.Movie.Title, "stream", index, "err", err)
			http.Error(w, "subtitles not available", http.StatusInternalServerError)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", subtitles.ContentType("vtt"))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// progress records where the player is: form fields item (the movie, or a
// later part's file), source (the version), position in seconds and event
// (start, progress or stop). The player sends it with fetch and, when the
// page goes away, with sendBeacon.
func (s *Server) progress(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.Catalog == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	it, ok := playstate.Resolve(s.Catalog.Snapshot(), r.PostFormValue("item"), r.PostFormValue("source"))
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	kind := map[string]playstate.Kind{"start": playstate.Start, "progress": playstate.Progress, "stop": playstate.Stop}[r.PostFormValue("event")]
	var pos *time.Duration
	if secs, err := strconv.ParseFloat(r.PostFormValue("position"), 64); err == nil && secs >= 0 {
		d := time.Duration(secs * float64(time.Second))
		pos = &d
	}
	d, err := playstate.Report(r.Context(), s.Store, sess.User.ID, it, kind, pos, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	switch kind {
	case playstate.Start:
		s.Log.Info("playback started", "item", it.Movie.Title, "user", sess.User.Name, "client", clientName)
	case playstate.Stop:
		s.Log.Info("playback stopped", "item", it.Movie.Title, "user", sess.User.Name, "position", d.Position.Round(time.Second),
			"played", d.Played)
	}
	w.WriteHeader(http.StatusNoContent)
}
