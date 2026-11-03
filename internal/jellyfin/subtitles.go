package jellyfin

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"homeflix/internal/subtitles"
)

func (s *Server) registerSubtitles(rt *Router) {
	for _, p := range []string{
		"/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/Stream.{format}",
		"/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/{startTicks}/Stream.{format}",
	} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.subtitleStream))
	}
}

// subtitleStream serves an embedded text subtitle track as a file. The
// start position is ignored: it only matters for transcoded streams that
// begin later, and homeflix serves the original file.
func (s *Server) subtitleStream(w http.ResponseWriter, r *http.Request, sess *Session) {
	index, err := strconv.Atoi(r.PathValue("index"))
	format := strings.ToLower(r.PathValue("format"))
	if err != nil || s.Subtitles == nil || subtitles.ContentType(format) == "" {
		http.NotFound(w, r)
		return
	}
	sources, ok := sourcesFor(s.Catalog.Snapshot(), normalizeID(r.PathValue("itemId")),
		normalizeID(r.PathValue("mediaSourceId")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	src := sources[0]
	path, err := s.Subtitles.Get(r.Context(), src.Part(), index, format)
	switch {
	case errors.Is(err, subtitles.ErrNotText):
		http.NotFound(w, r)
		return
	case err != nil:
		if r.Context().Err() == nil {
			s.Log.Warn("subtitles unavailable", "item", src.Movie.Title, "stream", index, "err", err)
			http.Error(w, "subtitles not available", http.StatusInternalServerError)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.Log.Info("subtitles", "item", src.Movie.Title, "stream", index, "format", format,
		"user", sess.User.Name, "client", sess.Token.Client)
	w.Header().Set("Content-Type", subtitles.ContentType(format))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
