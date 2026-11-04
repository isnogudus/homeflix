package store

import (
	"context"
	"time"
)

// Person is what TMDB knows about a member of cast or crew. Status is one
// of the metadata states MetaMatched, MetaNotFound and MetaError.
type Person struct {
	TMDbID    int
	Status    string
	Error     string
	Data      string // JSON
	Photo     string // file name in the metadata directory, "" if none
	FetchedAt time.Time
}

// AllPeople returns all people by TMDB ID.
func (s *Store) AllPeople(ctx context.Context) (map[int]Person, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT tmdb_id, status, error, data, photo, fetched_at FROM people")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]Person{}
	for rows.Next() {
		var p Person
		var fetched int64
		if err := rows.Scan(&p.TMDbID, &p.Status, &p.Error, &p.Data, &p.Photo, &fetched); err != nil {
			return nil, err
		}
		p.FetchedAt = time.UnixMilli(fetched).UTC()
		out[p.TMDbID] = p
	}
	return out, rows.Err()
}

// SavePerson stores a person, replacing what was there.
func (s *Store) SavePerson(ctx context.Context, p Person) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO people(tmdb_id, status, error, data, photo, fetched_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(tmdb_id) DO UPDATE SET status = excluded.status, error = excluded.error,
		data = excluded.data, photo = excluded.photo, fetched_at = excluded.fetched_at`,
		p.TMDbID, p.Status, p.Error, p.Data, p.Photo, p.FetchedAt.UnixMilli())
	return err
}

// DeletePeople forgets people by TMDB ID.
func (s *Store) DeletePeople(ctx context.Context, ids []int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "DELETE FROM people WHERE tmdb_id = ?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
