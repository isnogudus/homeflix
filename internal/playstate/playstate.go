// Package playstate keeps where users are in movies and what they have
// seen, with Jellyfin's resume rules. The Jellyfin API and the web client
// both go through it, so a movie stopped in one resumes in the other.
package playstate

import (
	"context"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/store"
)

// Jellyfin's default resume rules: positions in the first 5 % are not
// kept, from 90 % on the item counts as played, and items shorter than
// five minutes are never resumed.
const (
	MinResumeFraction = 0.05
	MaxResumeFraction = 0.90
	MinResumeRuntime  = 5 * time.Minute
	// Audiobooks: no position in the first five minutes, and played
	// within the last five (MinAudiobookResume, MaxAudiobookResume).
	MinAudiobookResume = 5 * time.Minute
	MaxAudiobookResume = 5 * time.Minute
)

// Item is something user data is kept for: a movie, an extra or a later
// part of a movie (addressed by its file ID).
type Item struct {
	ID      string
	Movie   *catalog.Movie
	Part    *catalog.PartRef // set for later parts
	Runtime time.Duration    // what positions refer to
}

// Runtime is the duration positions of a movie refer to: that of the
// first file of its default version.
func Runtime(m *catalog.Movie) time.Duration {
	if len(m.Versions) == 0 {
		return 0
	}
	// An audiobook plays as one stream over all its files.
	if m.Book != nil {
		return m.Book.Duration
	}
	return m.Versions[0].Parts[0].Info.Duration
}

// Resolve finds an item; sourceID, if set, picks the version whose
// runtime counts.
func Resolve(snap *catalog.Snapshot, itemID, sourceID string) (Item, bool) {
	if m, ok := snap.Item(itemID); ok {
		it := Item{ID: m.ID, Movie: m, Runtime: Runtime(m)}
		// A book plays as one stream; its source is the whole book.
		if _, v, ok := snap.Source(sourceID); ok && len(v.Parts) > 0 && m.Book == nil {
			it.Runtime = v.Parts[0].Info.Duration
		}
		return it, true
	}
	if r, ok := snap.Part(itemID); ok && r.Index > 0 {
		return Item{ID: itemID, Movie: r.Movie, Part: &r, Runtime: r.Part().Info.Duration}, true
	}
	return Item{}, false
}

// Kind says what a playback report is.
type Kind int

const (
	Start Kind = iota
	Progress
	Stop
)

// Report records a playback report at pos (nil if the client sent none).
// A stop past MaxResumeFraction marks the item played, and the movie too
// when it was the last part. Tracks shorter than MinResumeRuntime count as
// played once MinResumeFraction of them is reached; audiobooks follow the
// audiobook rules, also on progress reports. It returns the item's new
// state.
func Report(ctx context.Context, st *store.Store, userID string, it Item, kind Kind, pos *time.Duration, failed bool) (store.UserData, error) {
	d, err := st.GetUserData(ctx, userID, it.ID)
	if err != nil {
		return d, err
	}
	now := time.Now().UTC()
	d.LastPlayed = now
	finished := false
	isBook := it.Movie != nil && it.Movie.Book != nil
	isTrack := it.Movie != nil && it.Movie.Track != nil
	// A book is finished once per listening: not again by the stop that
	// follows the progress report that finished it.
	listening := d.Position > 0 || !d.Played
	if pos != nil && kind != Start && isBook && it.Runtime > 0 {
		// Moonfin's audiobook player reports progress but never a stop,
		// so the end of a book also counts while it plays.
		switch {
		case *pos < MinAudiobookResume:
			d.Position = 0
		case it.Runtime-*pos < MaxAudiobookResume:
			d.Position = 0
			finished = listening
		default:
			d.Position = *pos
		}
	} else if pos != nil && kind != Start {
		frac := 0.0
		if it.Runtime > 0 {
			frac = float64(*pos) / float64(it.Runtime)
		}
		switch {
		case isTrack && it.Runtime > 0 && it.Runtime < MinResumeRuntime && frac >= MinResumeFraction:
			// A song counts as heard once a little of it was, as in
			// Jellyfin; it keeps no position.
			d.Position = 0
			finished = kind == Stop
		case it.Runtime > 0 && frac >= MaxResumeFraction:
			d.Position = 0
			finished = kind == Stop
		case it.Runtime > 0 && (frac < MinResumeFraction || it.Runtime < MinResumeRuntime):
			d.Position = 0
		default:
			d.Position = *pos
		}
	}
	if finished && !failed {
		d.Played = true
		d.PlayCount++
	}
	if err := st.SaveUserData(ctx, userID, d); err != nil {
		return d, err
	}
	if finished && !failed && it.Part != nil && it.Part.IsLast() {
		movie, err := st.GetUserData(ctx, userID, it.Movie.ID)
		if err != nil {
			return d, err
		}
		movie.Played, movie.PlayCount, movie.Position, movie.LastPlayed = true, movie.PlayCount+1, 0, now
		if err := st.SaveUserData(ctx, userID, movie); err != nil {
			return d, err
		}
	}
	return d, nil
}

// RelatedIDs are the items whose state follows a movie's when it is marked
// played or unplayed by hand: the movie and all later parts of its versions.
func RelatedIDs(it Item) []string {
	ids := []string{it.ID}
	if it.Part != nil {
		return ids
	}
	for _, v := range it.Movie.Versions {
		for _, part := range v.Parts[1:] {
			ids = append(ids, part.File.ID)
		}
	}
	return ids
}

// SetPlayed marks an item, with its related items, played or unplayed and
// returns the new state of the item itself.
func SetPlayed(ctx context.Context, st *store.Store, userID string, it Item, played bool, now time.Time) (store.UserData, error) {
	var self store.UserData
	for _, id := range RelatedIDs(it) {
		d, err := st.GetUserData(ctx, userID, id)
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
		if err := st.SaveUserData(ctx, userID, d); err != nil {
			return self, err
		}
		if id == it.ID {
			self = d
		}
	}
	return self, nil
}
