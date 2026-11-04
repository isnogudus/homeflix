package jellyfin

import (
	"context"
	"net/http"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
)

// Jellyfin's default resume rules: positions in the first 5 % are not
// kept, from 90 % on the item counts as played, and items shorter than
// five minutes are never resumed.
const (
	minResumeFraction = 0.05
	maxResumeFraction = 0.90
	minResumeRuntime  = 5 * time.Minute
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

// playable is an item user data can be kept for: a movie, an extra or a
// later part of a movie.
type playable struct {
	id      string
	movie   *catalog.Movie
	part    *catalog.PartRef // set for later parts
	runtime time.Duration
}

// resolvePlayable finds an item; sourceID, if set, picks the version
// whose runtime counts.
func (s *Server) resolvePlayable(itemID, sourceID string) (playable, bool) {
	snap := s.Catalog.Snapshot()
	if m, ok := snap.Item(itemID); ok {
		p := playable{id: m.ID, movie: m, runtime: runtimeOf(m)}
		if _, v, ok := snap.Source(sourceID); ok && len(v.Parts) > 0 {
			p.runtime = v.Parts[0].Info.Duration
		}
		return p, true
	}
	if r, ok := snap.Part(itemID); ok && r.Index > 0 {
		return playable{id: itemID, movie: r.Movie, part: &r, runtime: r.Part().Info.Duration}, true
	}
	return playable{}, false
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

type reportKind int

const (
	reportStart reportKind = iota
	reportProgress
	reportStop
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
	ctx := r.Context()
	d, err := s.Store.GetUserData(ctx, sess.User.ID, p.id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := time.Now().UTC()
	d.LastPlayed = now
	finished := false
	if rep.PositionTicks != nil && kind != reportStart {
		pos := time.Duration(*rep.PositionTicks) * 100
		frac := 0.0
		if p.runtime > 0 {
			frac = float64(pos) / float64(p.runtime)
		}
		switch {
		case p.runtime > 0 && frac >= maxResumeFraction:
			d.Position = 0
			finished = kind == reportStop
		case p.runtime > 0 && (frac < minResumeFraction || p.runtime < minResumeRuntime):
			d.Position = 0
		default:
			d.Position = pos
		}
	}
	if finished && !rep.Failed {
		d.Played = true
		d.PlayCount++
	}
	if err := s.Store.SaveUserData(ctx, sess.User.ID, d); err != nil {
		s.internalError(w, r, err)
		return
	}
	if finished && p.part != nil && p.part.IsLast() {
		movie, err := s.Store.GetUserData(ctx, sess.User.ID, p.movie.ID)
		if err == nil {
			movie.Played, movie.PlayCount, movie.Position, movie.LastPlayed = true, movie.PlayCount+1, 0, now
			err = s.Store.SaveUserData(ctx, sess.User.ID, movie)
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	switch kind {
	case reportStart:
		s.Log.Info("playback started", "item", p.movie.Title, "user", sess.User.Name, "client", sess.Token.Client)
	case reportStop:
		s.Log.Info("playback stopped", "item", p.movie.Title, "user", sess.User.Name, "position", d.Position.Round(time.Second),
			"played", d.Played)
	}
	w.WriteHeader(http.StatusNoContent)
}

// relatedIDs are the items whose state follows a movie's when it is marked
// played or unplayed by hand: the movie and all later parts of its versions.
func relatedIDs(p playable) []string {
	ids := []string{p.id}
	if p.part != nil {
		return ids
	}
	for _, v := range p.movie.Versions {
		for _, part := range v.Parts[1:] {
			ids = append(ids, part.File.ID)
		}
	}
	return ids
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
		writeJSON(w, http.StatusOK, userDataDto(p.id, self, p.runtime))
	}
}

// setPlayed marks a movie or part, and the later parts of a movie, played
// or unplayed, and returns the new state of the item itself.
func (s *Server) setPlayed(ctx context.Context, userID string, p playable, played bool, now time.Time) (store.UserData, error) {
	var self store.UserData
	for _, id := range relatedIDs(p) {
		d, err := s.Store.GetUserData(ctx, userID, id)
		if err != nil {
			return self, err
		}
		d.Position = 0
		if played {
			if !d.Played {
				d.PlayCount++
			}
			d.Played, d.LastPlayed = true, now
		} else {
			d.Played, d.PlayCount, d.LastPlayed = false, 0, time.Time{}
		}
		if err := s.Store.SaveUserData(ctx, userID, d); err != nil {
			return self, err
		}
		if id == p.id {
			self = d
		}
	}
	return self, nil
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
		p = playable{id: c.ID}
	case !ok:
		http.NotFound(w, r)
		return
	}
	d, err := s.Store.GetUserData(r.Context(), u.ID, p.id)
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
	writeJSON(w, http.StatusOK, userDataDto(p.id, d, p.runtime))
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
