package jellyfin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"homeflix/internal/catalog"
	"homeflix/internal/profile"
	"homeflix/internal/store"
)

func (s *Server) registerPlayback(rt *Router) {
	rt.HandleFunc(http.MethodPost, "/Items/{itemId}/PlaybackInfo", s.authed(s.playbackInfo))
	rt.HandleFunc(http.MethodGet, "/Items/{itemId}/PlaybackInfo", s.authed(s.playbackInfo))
	rt.HandleFunc(http.MethodGet, "/Videos/{itemId}/stream", s.authed(s.stream))
	rt.HandleFunc(http.MethodGet, "/Videos/{itemId}/stream.{container}", s.authed(s.stream))
	rt.HandleFunc(http.MethodGet, "/Items/{itemId}/Download", s.authed(s.download))
	rt.HandleFunc(http.MethodGet, "/Videos/{itemId}/AdditionalParts", s.authed(s.additionalParts))
}

// playbackInfoRequest is the body of a PlaybackInfo request; query
// parameters of the same names fill in what the body leaves out.
type playbackInfoRequest struct {
	MaxStreamingBitrate int64
	AudioStreamIndex    *int
	SubtitleStreamIndex *int
	MediaSourceID       string `json:"MediaSourceId"`
	DeviceProfile       *profile.DeviceProfile
	EnableDirectPlay    *bool
	EnableDirectStream  *bool
}

// PlaybackInfoResponse lists the sources a client may play.
type PlaybackInfoResponse struct {
	MediaSources  []MediaSourceInfo `json:"MediaSources"`
	PlaySessionID string            `json:"PlaySessionId"`
}

// sourceRef is one playable file: a part of a version of a movie.
type sourceRef struct {
	catalog.PartRef
	ID string // media source ID clients use for it
}

// sourcesFor returns the files an item ID and an optional media source ID
// stand for: every version of a movie, or the one picked.
func sourcesFor(snap *catalog.Snapshot, itemID, sourceID string) ([]sourceRef, bool) {
	ref := func(p catalog.PartRef) sourceRef {
		id := p.Version.ID
		if p.Index > 0 {
			id = p.Part().File.ID
		}
		return sourceRef{PartRef: p, ID: id}
	}
	if sourceID != "" {
		if m, v, ok := snap.Source(sourceID); ok {
			return []sourceRef{ref(catalog.PartRef{Movie: m, Version: v})}, true
		}
		if p, ok := snap.Part(sourceID); ok {
			return []sourceRef{ref(p)}, true
		}
	}
	if m, ok := snap.Item(itemID); ok && len(m.Versions) > 0 {
		var out []sourceRef
		for _, v := range m.Versions {
			out = append(out, ref(catalog.PartRef{Movie: m, Version: v}))
		}
		return out, true
	}
	if p, ok := snap.Part(itemID); ok {
		return []sourceRef{ref(p)}, true
	}
	return nil, false
}

func (s *Server) playbackInfo(w http.ResponseWriter, r *http.Request, sess *Session) {
	var req playbackInfoRequest
	if r.Method == http.MethodPost && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}
	if req.MediaSourceID == "" {
		req.MediaSourceID = queryValue(r, "MediaSourceId")
	}
	if req.MaxStreamingBitrate == 0 {
		req.MaxStreamingBitrate, _ = strconv.ParseInt(queryValue(r, "MaxStreamingBitrate"), 10, 64)
	}
	if req.AudioStreamIndex == nil {
		if n, err := strconv.Atoi(queryValue(r, "AudioStreamIndex")); err == nil {
			req.AudioStreamIndex = &n
		}
	}
	if req.SubtitleStreamIndex == nil {
		if n, err := strconv.Atoi(queryValue(r, "SubtitleStreamIndex")); err == nil {
			req.SubtitleStreamIndex = &n
		}
	}

	itemID := normalizeID(r.PathValue("itemId"))
	sources, ok := sourcesFor(s.Catalog.Snapshot(), itemID, normalizeID(req.MediaSourceID))
	if !ok {
		http.NotFound(w, r)
		return
	}
	sessionID, err := store.NewID()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp := PlaybackInfoResponse{PlaySessionID: sessionID}
	for _, src := range sources {
		part := src.Part()
		dto := sourceDto(src.Movie, src.Version, src.Index, false)
		dto.ID = src.ID
		audio := -1
		if req.AudioStreamIndex != nil {
			audio = *req.AudioStreamIndex
			dto.DefaultAudioStreamIndex = intPtr(audio)
		}
		if req.SubtitleStreamIndex != nil {
			dto.DefaultSubtitleStreamIndex = intPtr(*req.SubtitleStreamIndex)
		}
		direct := true
		var reasons []string
		if req.DeviceProfile != nil {
			d := req.DeviceProfile.Evaluate(part.Info, audio, part.Info.BitRate, req.MaxStreamingBitrate)
			direct, reasons = d.DirectPlay, d.Reasons
		}
		if req.EnableDirectPlay != nil && !*req.EnableDirectPlay {
			direct = false
			reasons = append(reasons, "DirectPlayDisabled")
		}
		dto.SupportsDirectPlay = direct
		dto.SupportsDirectStream = direct && (req.EnableDirectStream == nil || *req.EnableDirectStream)
		if dto.SupportsDirectStream {
			dto.DirectStreamURL = fmt.Sprintf("/Videos/%s/stream?Static=true&MediaSourceId=%s&PlaySessionId=%s",
				itemID, url.QueryEscape(src.ID), sessionID)
		}
		s.Log.Info("playback info", "item", src.Movie.Title, "source", dto.Name, "part", src.Index+1,
			"container", dto.Container, "direct_play", direct, "reasons", strings.Join(reasons, ","),
			"client", sess.Token.Client, "user", sess.User.Name)
		resp.MediaSources = append(resp.MediaSources, dto)
	}
	writeJSON(w, http.StatusOK, resp)
}

// contentTypes maps file extensions to the types sent for direct play.
var contentTypes = map[string]string{
	".mkv": "video/x-matroska", ".mp4": "video/mp4", ".m4v": "video/x-m4v", ".mov": "video/quicktime",
	".avi": "video/x-msvideo", ".webm": "video/webm", ".mpg": "video/mpeg", ".mpeg": "video/mpeg",
	".ts": "video/mp2t", ".m2ts": "video/mp2t", ".wmv": "video/x-ms-wmv", ".flv": "video/x-flv",
	".ogv": "video/ogg", ".3gp": "video/3gpp",
}

// resolveFile finds the file a stream request is for.
func (s *Server) resolveFile(r *http.Request) (sourceRef, bool) {
	sources, ok := sourcesFor(s.Catalog.Snapshot(), normalizeID(r.PathValue("itemId")),
		normalizeID(queryValue(r, "MediaSourceId")))
	if !ok {
		return sourceRef{}, false
	}
	return sources[0], true
}

// stream serves the original file; ranges let clients seek. Requests for
// remuxing or transcoding get the original as well.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.serveFile(w, r, sess, false)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.serveFile(w, r, sess, true)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, sess *Session, attachment bool) {
	src, ok := s.resolveFile(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := src.Part().File.Path
	f, err := os.Open(path)
	if err != nil {
		s.Log.Error("open media file", "path", path, "err", err)
		http.Error(w, "media file not available", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ext := strings.ToLower(filepath.Ext(path))
	ct := contentTypes[ext]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if attachment {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
		s.Log.Info("download", "item", src.Movie.Title, "user", sess.User.Name, "client", sess.Token.Client)
	}
	if r.Header.Get("Range") == "" && r.Method == http.MethodGet && !attachment {
		s.Log.Info("stream", "item", src.Movie.Title, "part", src.Index+1, "user", sess.User.Name,
			"client", sess.Token.Client)
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) additionalParts(w http.ResponseWriter, r *http.Request, sess *Session) {
	snap := s.Catalog.Snapshot()
	id := normalizeID(r.PathValue("itemId"))
	var movie *catalog.Movie
	var version *catalog.Version
	if m, ok := snap.Item(id); ok && len(m.Versions) > 0 {
		movie, version = m, m.Versions[0]
	} else if m, v, ok := snap.Source(id); ok {
		movie, version = m, v
	} else if p, ok := snap.Part(id); ok {
		movie, version = p.Movie, p.Version
	} else {
		http.NotFound(w, r)
		return
	}
	user, ok := s.userState(w, r, sess.User)
	if !ok {
		return
	}
	// As in Jellyfin, parts come with all fields: clients queue them for
	// playback without asking for details.
	items := []BaseItemDto{}
	for i := 1; i < len(version.Parts); i++ {
		items = append(items, s.partDto(catalog.PartRef{Movie: movie, Version: version, Index: i}, nil, sess, user))
	}
	writeJSON(w, http.StatusOK, QueryResult[BaseItemDto]{Items: items, TotalRecordCount: len(items)})
}
