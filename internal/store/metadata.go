package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Metadata states.
const (
	MetaMatched  = "matched"
	MetaNotFound = "not_found"
	MetaError    = "error"
	// MetaManual marks a match set by hand that still has to be fetched.
	MetaManual = "manual"
)

// Metadata is what TMDB knows about a movie.
type Metadata struct {
	ItemID    string
	Source    string // "imdb", "search" or "manual"
	MatchKey  string
	Status    string
	Error     string
	TMDbID    int
	Data      string // JSON
	Poster    string // file name in the metadata directory, "" if none
	Backdrop  string
	FetchedAt time.Time
}

const metadataColumns = "item_id, source, match_key, status, error, tmdb_id, data, poster, backdrop, fetched_at"

func scanMetadata(row interface{ Scan(...any) error }) (Metadata, error) {
	var m Metadata
	var tmdbID sql.NullInt64
	var fetched int64
	if err := row.Scan(&m.ItemID, &m.Source, &m.MatchKey, &m.Status, &m.Error, &tmdbID, &m.Data,
		&m.Poster, &m.Backdrop, &fetched); err != nil {
		return Metadata{}, err
	}
	m.TMDbID = int(tmdbID.Int64)
	m.FetchedAt = time.UnixMilli(fetched).UTC()
	return m, nil
}

// AllMetadata returns the metadata of all items by item ID.
func (s *Store) AllMetadata(ctx context.Context) (map[string]Metadata, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+metadataColumns+" FROM metadata")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Metadata{}
	for rows.Next() {
		m, err := scanMetadata(rows)
		if err != nil {
			return nil, err
		}
		out[m.ItemID] = m
	}
	return out, rows.Err()
}

// GetMetadata returns the metadata of one item.
func (s *Store) GetMetadata(ctx context.Context, itemID string) (Metadata, error) {
	m, err := scanMetadata(s.db.QueryRowContext(ctx, "SELECT "+metadataColumns+" FROM metadata WHERE item_id = ?", itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return Metadata{}, ErrNotFound
	}
	return m, err
}

// SaveMetadata stores metadata, replacing what was there.
func (s *Store) SaveMetadata(ctx context.Context, m Metadata) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO metadata(`+metadataColumns+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET source = excluded.source, match_key = excluded.match_key,
		status = excluded.status, error = excluded.error, tmdb_id = excluded.tmdb_id, data = excluded.data,
		poster = excluded.poster, backdrop = excluded.backdrop, fetched_at = excluded.fetched_at`,
		m.ItemID, m.Source, m.MatchKey, m.Status, m.Error, nullInt(m.TMDbID), m.Data, m.Poster, m.Backdrop,
		m.FetchedAt.UnixMilli())
	return err
}

// DeleteMetadata forgets the metadata of an item, so it is matched again.
func (s *Store) DeleteMetadata(ctx context.Context, itemID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM metadata WHERE item_id = ?", itemID)
	return err
}

// Movies returns the present top-level movies without their files.
func (s *Store) Movies(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+itemColumns+
		" FROM items WHERE missing_since IS NULL AND parent_id IS NULL ORDER BY sort_title")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}
