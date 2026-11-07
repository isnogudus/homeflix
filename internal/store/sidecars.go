package store

import (
	"context"
	"fmt"
	"time"
)

// Sidecar is a file next to the media that describes or illustrates it:
// an .nfo or metadata.json file, a picture or lyrics.
type Sidecar struct {
	Path      string
	LibraryID string
	Dir       string // directory holding it
	Name      string // base name in lower case
	Size      int64
	ModTime   time.Time
	Data      string // what a description holds, as JSON; "" for pictures and lyrics
}

// Sidecars returns the sidecar files of a library, or of all libraries
// for "".
func (s *Store) Sidecars(ctx context.Context, libraryID string) ([]Sidecar, error) {
	q := "SELECT path, library_id, dir, name, size, mtime, data FROM sidecars"
	var args []any
	if libraryID != "" {
		q += " WHERE library_id = ?"
		args = append(args, libraryID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY path", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sidecar
	for rows.Next() {
		var sc Sidecar
		var mtime int64
		if err := rows.Scan(&sc.Path, &sc.LibraryID, &sc.Dir, &sc.Name, &sc.Size, &mtime, &sc.Data); err != nil {
			return nil, err
		}
		sc.ModTime = time.UnixMilli(mtime).UTC()
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ReplaceSidecars makes the stored sidecars of a library the given ones.
func (s *Store) ReplaceSidecars(ctx context.Context, libraryID string, list []Sidecar) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sidecars WHERE library_id = ?", libraryID); err != nil {
		return err
	}
	for _, sc := range list {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sidecars(path, library_id, dir, name, size, mtime, data)
			VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(path) DO NOTHING`,
			sc.Path, libraryID, sc.Dir, sc.Name, sc.Size, sc.ModTime.UnixMilli(), sc.Data); err != nil {
			return fmt.Errorf("insert sidecar %s: %w", sc.Path, err)
		}
	}
	return tx.Commit()
}
