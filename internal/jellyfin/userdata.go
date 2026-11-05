package jellyfin

import (
	"context"
	"net/http"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/playstate"
	"homeflix/internal/store"
)

func (s *Server) registerUserData(rt *Router) {
	rt.HandleFunc(http.MethodPost, "/Sessions/Playing", s.authed(s.playbackStart))
	rt.HandleFunc(http.MethodPost, "/Sessions/Playing/Progress", s.authed(s.playbackProgress))
	rt.HandleFunc(http.MethodPost, "/Sessions/Playing/Stopped", s.authed(s.playbackStopped))
	rt.HandleFunc(http.MethodPost, "/Sessions/Playing/Ping", s.authed(s.playbackPing))
	for _, p := range []string{"/UserPlayedItems/{itemId}", "/Users/{userId}/PlayedItems/{itemId}"} {
		rt.HandleFunc(http.MethodPost, p, s.authed(s.markPlayed(true)))
		rt.HandleFunc(http.MethodDelete, p, s.authed(s.markPlayed(false)))
	}
	for _, p := range []string{"/UserFavoriteItems/{itemId}", "/Users/{userId}/FavoriteItems/{itemId}"} {
		rt.HandleFunc(http.MethodPost, p, s.authed(s.markFavorite(true)))
		rt.HandleFunc(http.MethodDelete, p, s.authed(s.markFavorite(false)))
	}
	for _, p := range []string{"/UserItems/{itemId}/UserData", "/Users/{userId}/Items/{itemId}/UserData"} {
		rt.HandleFunc(http.MethodGet, p, s.authed(s.getUserItemData))
		rt.HandleFunc(http.MethodPost, p, s.authed(s.updateUserItemData))
	}
	for _, p := range []string{"/UserItems/{itemId}/Rating", "/Users/{userId}/Items/{itemId}/Rating"} {
		// Likes are not kept; the current state is returned.
		rt.HandleFunc(http.MethodPost, p, s.authed(s.getUserItemData))
		rt.HandleFunc(http.MethodDelete, p, s.authed(s.getUserItemData))
	}
}

// playable is an item user data can be kept for.
type playable = playstate.Item

// resolvePlayable finds an item; sourceID, if set, picks the version
// whose runtime counts.
func (s *Server) resolvePlayable(itemID, sourceID string) (playable, bool) {
	return playstate.Resolve(s.Catalog.Snapshot(), itemID, sourceID)
}

// playbackReport is the part of Jellyfin's PlaybackStartInfo,
// PlaybackProgressInfo and PlaybackStopInfo that homeflix uses.
type playbackReport struct {
	ItemID        string `json:"ItemId"`
	MediaSourceID string `json:"MediaSourceId"`
	PositionTicks *int64
	IsPaused      bool
	PlaySessionID string `json:"PlaySessionId"`
	Failed        bool
}

type reportKind = playstate.Kind

const (
	reportStart    = playstate.Start
	reportProgress = playstate.Progress
	reportStop     = playstate.Stop
)

func (s *Server) playbackStart(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.playbackReport(w, r, sess, reportStart)
}

func (s *Server) playbackProgress(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.playbackReport(w, r, sess, reportProgress)
}

func (s *Server) playbackStopped(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.playbackReport(w, r, sess, reportStop)
}

func (s *Server) playbackPing(w http.ResponseWriter, r *http.Request, sess *Session) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) playbackReport(w http.ResponseWriter, r *http.Request, sess *Session, kind reportKind) {
	var rep playbackReport
	if !decodeJSON(w, r, &rep) {
		return
	}
	p, ok := s.resolvePlayable(normalizeID(rep.ItemID), normalizeID(rep.MediaSourceID))
	if !ok {
		// Reports for unknown items (gone since playback started) are
		// accepted and dropped.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var pos *time.Duration
	if rep.PositionTicks != nil {
		d := time.Duration(*rep.PositionTicks) * 100
		pos = &d
	}
	d, err := playstate.Report(r.Context(), s.Store, sess.User.ID, p, kind, pos, rep.Failed)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	switch kind {
	case reportStart:
		s.Log.Info("playback started", "item", p.Movie.Title, "user", sess.User.Name, "client", sess.Token.Client)
	case reportStop:
		s.Log.Info("playback stopped", "item", p.Movie.Title, "user", sess.User.Name, "position", d.Position.Round(time.Second),
			"played", d.Played)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) markPlayed(played bool) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *Session) {
		u, ok := s.targetUser(w, r, sess)
		if !ok {
			return
		}
		id := normalizeID(r.PathValue("itemId"))
		now := time.Now().UTC()
		// A collection is played when all its movies are.
		if c, ok := s.Catalog.Snapshot().Collection(id); ok {
			for _, m := range c.Movies {
				if p, ok := s.resolvePlayable(m.ID, ""); ok {
					if _, err := s.setPlayed(r.Context(), u.ID, p, played, now); err != nil {
						s.internalError(w, r, err)
						return
					}
				}
			}
			s.writeCollectionUserData(w, r, u, c)
			return
		}
		p, ok := s.resolvePlayable(id, "")
		if !ok {
			http.NotFound(w, r)
			return
		}
		self, err := s.setPlayed(r.Context(), u.ID, p, played, now)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, userDataDto(p.ID, self, p.Runtime))
	}
}

// setPlayed marks a movie or part, and the later parts of a movie, played
// or unplayed, and returns the new state of the item itself.
func (s *Server) setPlayed(ctx context.Context, userID string, p playable, played bool, now time.Time) (store.UserData, error) {
	return playstate.SetPlayed(ctx, s.Store, userID, p, played, now)
}

// writeCollectionUserData answers with the user's state of a collection.
func (s *Server) writeCollectionUserData(w http.ResponseWriter, r *http.Request, u store.User, c *catalog.Collection) {
	user, ok := s.userState(w, r, u)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, collectionUserData(c, user))
}

func (s *Server) markFavorite(favorite bool) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *Session) {
		s.changeUserData(w, r, sess, func(d *store.UserData, _ playable) { d.IsFavorite = favorite })
	}
}

// changeUserData applies change to the state of the item in the path and
// returns the result.
func (s *Server) changeUserData(w http.ResponseWriter, r *http.Request, sess *Session, change func(*store.UserData, playable)) {
	u, ok := s.targetUser(w, r, sess)
	if !ok {
		return
	}
	id := normalizeID(r.PathValue("itemId"))
	p, ok := s.resolvePlayable(id, "")
	c, isCollection := s.Catalog.Snapshot().Collection(id)
	switch {
	case isCollection:
		// A collection keeps only its favourite flag of its own.
		p = playable{ID: c.ID}
	case !ok:
		http.NotFound(w, r)
		return
	}
	d, err := s.Store.GetUserData(r.Context(), u.ID, p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if change != nil {
		change(&d, p)
		if err := s.Store.SaveUserData(r.Context(), u.ID, d); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if isCollection {
		s.writeCollectionUserData(w, r, u, c)
		return
	}
	writeJSON(w, http.StatusOK, userDataDto(p.ID, d, p.Runtime))
}

func (s *Server) getUserItemData(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.changeUserData(w, r, sess, nil)
}

// updateUserItemDataRequest holds the fields of UpdateUserItemDataDto a
// client may set; absent fields stay as they are.
type updateUserItemDataRequest struct {
	PlaybackPositionTicks *int64
	PlayCount             *int
	IsFavorite            *bool
	Played                *bool
	LastPlayedDate        *time.Time
}

func (s *Server) updateUserItemData(w http.ResponseWriter, r *http.Request, sess *Session) {
	var req updateUserItemDataRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.changeUserData(w, r, sess, func(d *store.UserData, _ playable) {
		if req.PlaybackPositionTicks != nil {
			d.Position = time.Duration(max(*req.PlaybackPositionTicks, 0)) * 100
		}
		if req.PlayCount != nil {
			d.PlayCount = max(*req.PlayCount, 0)
		}
		if req.IsFavorite != nil {
			d.IsFavorite = *req.IsFavorite
		}
		if req.Played != nil {
			d.Played = *req.Played
		}
		if req.LastPlayedDate != nil {
			d.LastPlayed = req.LastPlayedDate.UTC()
		}
	})
}
