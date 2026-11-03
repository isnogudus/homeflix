package jellyfin

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"homeflix/internal/images"
)

func (s *Server) registerImages(rt *Router) {
	// Images are served without authentication, as by Jellyfin: clients
	// load them through image caches that do not send credentials.
	rt.HandleFunc(http.MethodGet, "/Items/{itemId}/Images/{type}", s.itemImage)
	rt.HandleFunc(http.MethodGet, "/Items/{itemId}/Images/{type}/{index}", s.itemImage)
}

func (s *Server) itemImage(w http.ResponseWriter, r *http.Request) {
	kind, ok := images.ParseKind(r.PathValue("type"))
	if !ok || s.Images == nil {
		http.NotFound(w, r)
		return
	}
	if idx := r.PathValue("index"); idx != "" && idx != "0" {
		http.NotFound(w, r)
		return
	}
	snap := s.Catalog.Snapshot()
	id := normalizeID(r.PathValue("itemId"))
	m, ok := snap.Item(id)
	if !ok {
		// Later parts of a movie show the movie's artwork.
		p, isPart := snap.Part(id)
		if !isPart {
			http.NotFound(w, r)
			return
		}
		m = p.Movie
	}
	if kind != images.Primary && m.IsExtra() {
		http.NotFound(w, r)
		return
	}
	tag := images.Tag(m, kind)
	if tag == "" {
		http.NotFound(w, r)
		return
	}
	size := images.Size{
		MaxWidth: intParam(r, "maxWidth", 0), MaxHeight: intParam(r, "maxHeight", 0),
		Width: intParam(r, "width", 0), Height: intParam(r, "height", 0),
		FillWidth: intParam(r, "fillWidth", 0), FillHeight: intParam(r, "fillHeight", 0),
		Quality: intParam(r, "quality", 0),
	}
	path, err := s.Images.Get(r.Context(), m, kind, size)
	if err != nil {
		if errors.Is(err, images.ErrNoImage) || r.Context().Err() != nil {
			http.NotFound(w, r)
			return
		}
		s.Log.Warn("image unavailable", "item", m.Title, "type", kind, "err", err)
		http.NotFound(w, r)
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
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+tag+`"`)
	if strings.EqualFold(queryValue(r, "tag"), tag) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
