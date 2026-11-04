package jellyfin

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"homeflix/internal/catalog"
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
	if f, ok := snap.Person(id); ok {
		s.personImage(w, r, f, kind)
		return
	}
	if c, ok := snap.Collection(id); ok {
		s.collectionImage(w, r, c, kind)
		return
	}
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
	path, err := s.Images.Get(r.Context(), m, kind, imageSize(r))
	if err != nil {
		if errors.Is(err, images.ErrNoImage) || r.Context().Err() != nil {
			http.NotFound(w, r)
			return
		}
		s.Log.Warn("image unavailable", "item", m.Title, "type", kind, "err", err)
		http.NotFound(w, r)
		return
	}
	s.serveImage(w, r, path, tag)
}

func imageSize(r *http.Request) images.Size {
	return images.Size{
		MaxWidth: intParam(r, "maxWidth", 0), MaxHeight: intParam(r, "maxHeight", 0),
		Width: intParam(r, "width", 0), Height: intParam(r, "height", 0),
		FillWidth: intParam(r, "fillWidth", 0), FillHeight: intParam(r, "fillHeight", 0),
		Quality: intParam(r, "quality", 0),
	}
}

// personImage serves the portrait of a person.
func (s *Server) personImage(w http.ResponseWriter, r *http.Request, f *catalog.Facet, kind images.Kind) {
	tag := images.PhotoTag(f)
	if kind != images.Primary || tag == "" {
		http.NotFound(w, r)
		return
	}
	path, err := s.Images.Photo(f, imageSize(r))
	if err != nil {
		s.Log.Warn("image unavailable", "person", f.Name, "err", err)
		http.NotFound(w, r)
		return
	}
	s.serveImage(w, r, path, tag)
}

// collectionImage serves the poster or backdrop of a collection.
func (s *Server) collectionImage(w http.ResponseWriter, r *http.Request, c *catalog.Collection, kind images.Kind) {
	tag := images.CollectionTag(c, kind)
	if tag == "" || (kind != images.Primary && kind != images.Backdrop && kind != images.Thumb) {
		http.NotFound(w, r)
		return
	}
	path, err := s.Images.CollectionImage(r.Context(), c, kind, imageSize(r))
	if err != nil {
		if !errors.Is(err, images.ErrNoImage) && r.Context().Err() == nil {
			s.Log.Warn("image unavailable", "collection", c.Name, "type", kind, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	s.serveImage(w, r, path, tag)
}

// serveImage sends a cached JPEG; with the current tag in the request,
// clients may keep it forever.
func (s *Server) serveImage(w http.ResponseWriter, r *http.Request, path, tag string) {
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
