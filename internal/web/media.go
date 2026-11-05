package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/media"
	"homeflix/internal/playstate"
	"homeflix/internal/subtitles"
)

// playability says whether a browser can play a file as it is, and if not
// why: a text key and the codec or container in the way.
type playability struct {
	OK     bool
	Reason string // "needsRemux", "needsAudio", "needsVideo"
	Detail string
}

// browserAudio are the audio codecs every current browser decodes.
var browserAudio = map[string]bool{"aac": true, "mp3": true, "opus": true, "flac": true, "vorbis": true}

// directPlay decides whether browsers can play a part as it is. It is
// strict: what passes plays in Safari, Chrome, Firefox and Edge. Files
// that need their container changed, their audio converted or their video
// transcoded wait for the HLS pipeline of the next milestones.
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
var contentTypes = map[string]string{".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm"}

// stream serves a file of a movie for the player, with ranges for seeking.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, sess *session) {
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
	if r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
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
