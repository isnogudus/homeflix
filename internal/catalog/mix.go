package catalog

import (
	"math/rand/v2"
	"sort"
)

// InstantMix returns up to limit tracks that go with the seeds (tracks,
// albums, artists or genres, by ID): tracks of the same album artists and
// genres, shuffled, without the seed tracks themselves. Nothing is looked
// up outside; similarity is what the tags share.
func (s *Snapshot) InstantMix(seedIDs []string, limit int) []*Movie {
	if s.music == nil {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	artists, genres, exclude := map[string]bool{}, map[string]bool{}, map[string]bool{}
	addTrack := func(t *Movie) {
		exclude[t.ID] = true
		for _, a := range t.Track.AlbumArtists {
			artists[a.ID] = true
		}
		for _, a := range t.Track.Artists {
			artists[a.ID] = true
		}
		for _, g := range t.Track.Meta.Genres {
			genres[MusicGenreID(g)] = true
		}
		if al := t.Track.Album; al != nil {
			for id := range al.genreIDs {
				genres[id] = true
			}
		}
	}
	for _, id := range seedIDs {
		if m, ok := s.byID[id]; ok && m.Track != nil {
			addTrack(m)
		} else if al, ok := s.music.album[id]; ok {
			for _, t := range al.Tracks {
				addTrack(t)
			}
			// The album's own tracks belong in a mix made from it.
			for _, t := range al.Tracks {
				delete(exclude, t.ID)
			}
		} else if a, ok := s.music.artist[id]; ok {
			artists[id] = true
			for _, g := range a.Genres {
				genres[MusicGenreID(g)] = true
			}
		} else if _, ok := s.music.genres[id]; ok {
			genres[id] = true
		}
	}
	type scored struct {
		t     *Movie
		score float64
	}
	var cands []scored
	for _, e := range s.music.entries {
		if e.typ != TypeAudio || exclude[e.id] {
			continue
		}
		sc := 0.0
		for id := range e.artists {
			if artists[id] {
				sc += 2
				break
			}
		}
		for id := range e.genres {
			if genres[id] {
				sc++
				break
			}
		}
		if sc > 0 {
			cands = append(cands, scored{e.item.Item, sc + rand.Float64()*1.5})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > limit*3 {
		cands = cands[:limit*3]
	}
	rand.Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]*Movie, len(cands))
	for i, c := range cands {
		out[i] = c.t
	}
	return out
}

// SimilarArtists returns artists sharing genres with a, most shared first.
func (s *Snapshot) SimilarArtists(a *Artist, limit int) []*Artist {
	if s.music == nil {
		return nil
	}
	mine := map[string]bool{}
	for _, g := range a.Genres {
		mine[MusicGenreID(g)] = true
	}
	type scored struct {
		a *Artist
		n int
	}
	var cands []scored
	for _, o := range s.music.artists {
		if o == a || !o.albumArtist {
			continue
		}
		n := 0
		for _, g := range o.Genres {
			if mine[MusicGenreID(g)] {
				n++
			}
		}
		if n > 0 {
			cands = append(cands, scored{o, n})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].n > cands[j].n })
	var out []*Artist
	for _, c := range cands {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, c.a)
	}
	return out
}

// SimilarAlbums returns albums sharing genres with al, of other artists
// first, most shared first.
func (s *Snapshot) SimilarAlbums(al *Album, limit int) []*Album {
	if s.music == nil {
		return nil
	}
	type scored struct {
		a *Album
		n int
	}
	var cands []scored
	for _, o := range s.music.albums {
		if o == al {
			continue
		}
		n := 0
		for id := range o.genreIDs {
			if al.genreIDs[id] {
				n += 2
			}
		}
		for id := range o.artistIDs {
			if al.artistIDs[id] {
				n++
				break
			}
		}
		if n > 0 {
			cands = append(cands, scored{o, n})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].n > cands[j].n })
	var out []*Album
	for _, c := range cands {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, c.a)
	}
	return out
}
