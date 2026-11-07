package jellyfin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/hls"
)

func (s *Server) registerAudio(rt *Router) {
	for _, p := range []string{"/Audio/{itemId}/main.m3u8", "/Audio/{itemId}/master.m3u8"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.audioPlaylist))
	}
	rt.HandleFunc(http.MethodGet, "/Audio/{itemId}/hls/{segment}", s.authed(s.audioSegment))
	rt.HandleFunc(http.MethodGet, "/Audio/{itemId}/universal", s.authed(s.audioUniversal))
}

// isAudioItem reports whether a playable item is a track or audiobook.
func isAudioItem(m *catalog.Movie) bool { return m != nil && (m.Track != nil || m.Book != nil) }

// audioSource describes the AAC conversion of a track or a whole
// audiobook.
func audioSource(m *catalog.Movie, owner string) (hls.Source, bool) {
	if len(m.Versions) == 0 {
		return hls.Source{}, false
	}
	v := m.Versions[0]
	first := v.Parts[0]
	audio := first.Info.DefaultAudioIndex()
	if audio < 0 {
		return hls.Source{}, false
	}
	src := hls.Source{FileID: first.File.ID, Path: first.File.Path, Size: first.File.Size, ModTime: first.File.ModTime,
		Duration: v.Duration, Audio: audio, AudioOnly: true, Owner: owner}
	key := []string{}
	for _, p := range v.Parts {
		key = append(key, fmt.Sprintf("%s-%d-%d", p.File.ID, p.File.Size, p.File.ModTime.UnixMilli()))
	}
	src.Key = strings.Join(key, "+")
	if len(v.Parts) > 1 {
		for _, p := range v.Parts {
			src.Concat = append(src.Concat, p.File.Path)
		}
		src.Key = fmt.Sprintf("book-%s-%d", m.ID, len(v.Parts)) + "-" + key[len(key)-1]
	}
	return src, true
}

// needsConversion reports whether an audio item cannot be served as one
// file: an audiobook made of several files plays only converted.
func needsConversion(m *catalog.Movie) bool {
	return m.Book != nil && len(m.Versions) > 0 && len(m.Versions[0].Parts) > 1
}

// audioTranscodingURL is the playlist of the AAC conversion of an item,
// with the token in the URL: players follow it, and the segments it lists,
// without the client's headers.
func audioTranscodingURL(itemID, sourceID, playSession, token string) string {
	q := url.Values{}
	q.Set("MediaSourceId", sourceID)
	q.Set("PlaySessionId", playSession)
	q.Set("AudioCodec", "aac")
	q.Set("TranscodingContainer", "ts")
	q.Set("ApiKey", token)
	return "/Audio/" + itemID + "/main.m3u8?" + q.Encode()
}

// audioItem finds the track or audiobook of a request.
func (s *Server) audioItem(r *http.Request) (*catalog.Movie, bool) {
	m, ok := s.Catalog.Snapshot().Item(normalizeID(r.PathValue("itemId")))
	return m, ok && isAudioItem(m)
}

// mayConvertAudio reports whether the user may have audio converted:
// Jellyfin's EnableAudioPlaybackTranscoding, given with the levels that
// convert anything.
func mayConvertAudio(sess *Session) bool { return sess.User.Conversion.Remux() }

func (s *Server) audioPlaylist(w http.ResponseWriter, r *http.Request, sess *Session) {
	m, ok := s.audioItem(r)
	if !ok || s.HLS == nil {
		http.NotFound(w, r)
		return
	}
	if !mayConvertAudio(sess) {
		http.Error(w, "this account plays music and audiobooks only as they are", http.StatusForbidden)
		return
	}
	src, ok := audioSource(m, sess.Token.DeviceID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	segs, err := s.HLS.Segments(r.Context(), src)
	if err != nil {
		http.Error(w, "this file cannot be converted", http.StatusUnprocessableEntity)
		return
	}
	s.Log.Info("audio playlist", "item", m.Title, "user", sess.User.Name, "client", sess.Token.Client)
	q := url.Values{}
	q.Set("ApiKey", requestToken(r))
	if v := queryValue(r, "MediaSourceId"); v != "" {
		q.Set("MediaSourceId", v)
	}
	suffix := "?" + q.Encode()
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(hls.Playlist(segs, func(i int) string { return "hls/" + strconv.Itoa(i) + ".ts" + suffix }))
}

func (s *Server) audioSegment(w http.ResponseWriter, r *http.Request, sess *Session) {
	m, ok := s.audioItem(r)
	if !ok || s.HLS == nil {
		http.NotFound(w, r)
		return
	}
	if !mayConvertAudio(sess) {
		http.Error(w, "this account plays music and audiobooks only as they are", http.StatusForbidden)
		return
	}
	seg := r.PathValue("segment")
	n, err := strconv.Atoi(strings.TrimSuffix(seg, ".ts"))
	if err != nil || !strings.HasSuffix(seg, ".ts") {
		http.NotFound(w, r)
		return
	}
	src, ok := audioSource(m, sess.Token.DeviceID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	path, err := s.HLS.Segment(r.Context(), src, n)
	switch {
	case errors.Is(err, hls.ErrBusy):
		w.Header().Set("Retry-After", "10")
		http.Error(w, "the server is busy converting other files", http.StatusServiceUnavailable)
		return
	case err != nil:
		if r.Context().Err() == nil {
			s.Log.Warn("segment failed", "item", m.Title, "segment", n, "err", err)
			http.Error(w, "segment not available", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

// audioUniversal serves a track or audiobook as the client can play it:
// the file itself when its container and codec are in the lists the
// client sends, else the AAC conversion (redirected to its playlist).
func (s *Server) audioUniversal(w http.ResponseWriter, r *http.Request, sess *Session) {
	m, ok := s.audioItem(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !needsConversion(m) && universalDirect(m, listParam(r, "Container"), listParam(r, "AudioCodec"),
		queryValue(r, "TranscodingProtocol")) {
		s.serveFile(w, r, sess, false)
		return
	}
	if s.HLS == nil || !mayConvertAudio(sess) {
		http.Error(w, "this file cannot be played without conversion", http.StatusForbidden)
		return
	}
	target := audioTranscodingURL(m.ID, m.ID, strconv.FormatInt(time.Now().UnixNano(), 36), requestToken(r))
	http.Redirect(w, r, target, http.StatusFound)
}

// universalDirect decides /Audio/universal: containers lists what the
// client plays ("mp3", "flac", "m4a|aac" pairs of container and codec
// are also accepted), codecs the codecs; without lists everything plays.
func universalDirect(m *catalog.Movie, containers, codecs []string, _ string) bool {
	info := m.Versions[0].Info()
	var codec string
	for _, st := range info.Streams {
		if st.Type == "Audio" {
			codec = st.Codec
			break
		}
	}
	if len(containers) == 0 {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(fileExt(m.Versions[0].Parts[0].File.Path)), ".")
	for _, c := range containers {
		cont, cod, paired := strings.Cut(strings.ToLower(c), "|")
		if !strings.EqualFold(cont, ext) && !containsFold(strings.Split(info.Container, ","), cont) {
			continue
		}
		if paired && !strings.EqualFold(cod, codec) {
			continue
		}
		if !paired && len(codecs) > 0 && !containsFold(codecs, codec) && !containsFold(codecs, "copy") {
			// The codec list names what the client wants a conversion
			// in; the container list alone says what plays directly.
			return codecPlaysIn(cont, codec)
		}
		return true
	}
	return false
}

// codecPlaysIn reports whether codec is what a container of that name
// usually holds, for clients that list containers only.
func codecPlaysIn(container, codec string) bool {
	switch container {
	case "mp3":
		return codec == "mp3"
	case "flac":
		return codec == "flac"
	case "m4a", "m4b", "aac", "mp4":
		return codec == "aac" || codec == "alac"
	case "ogg", "oga":
		return codec == "vorbis" || codec == "opus" || codec == "flac"
	case "opus":
		return codec == "opus"
	case "wav":
		return strings.HasPrefix(codec, "pcm_")
	case "wma", "asf":
		return strings.HasPrefix(codec, "wma")
	}
	return false
}

func fileExt(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 && !strings.ContainsRune(p[i:], '/') {
		return p[i:]
	}
	return ""
}
