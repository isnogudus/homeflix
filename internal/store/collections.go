package store

import (
	"context"
	"time"
)

// Collection is what TMDB knows about a film series. Status is one of the
// metadata states MetaMatched, MetaNotFound and MetaError.
type Collection struct {
	TMDbID    int
	Status    string
	Error     string
	Data      string // JSON
	Poster    string // file names in the metadata directory, "" if none
	Backdrop  string
	FetchedAt time.Time
}

// AllCollections returns all collections by TMDB ID.
func (s *Store) AllCollections(ctx context.Context) (map[int]Collection, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT tmdb_id, status, error, data, poster, backdrop, fetched_at FROM collections")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]Collection{}
	for rows.Next() {
		var c Collection
		var fetched int64
		if err := rows.Scan(&c.TMDbID, &c.Status, &c.Error, &c.Data, &c.Poster, &c.Backdrop, &fetched); err != nil {
			return nil, err
		}
		c.FetchedAt = time.UnixMilli(fetched).UTC()
		out[c.TMDbID] = c
	}
	return out, rows.Err()
}

// SaveCollection stores a collection, replacing what was there.
func (s *Store) SaveCollection(ctx context.Context, c Collection) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO collections(tmdb_id, status, error, data, poster, backdrop, fetched_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tmdb_id) DO UPDATE SET status = excluded.status, error = excluded.error,
		data = excluded.data, poster = excluded.poster, backdrop = excluded.backdrop,
		fetched_at = excluded.fetched_at`,
		c.TMDbID, c.Status, c.Error, c.Data, c.Poster, c.Backdrop, c.FetchedAt.UnixMilli())
	return err
}

// DeleteCollections forgets collections by TMDB ID.
func (s *Store) DeleteCollections(ctx context.Context, ids []int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "DELETE FROM collections WHERE tmdb_id = ?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
