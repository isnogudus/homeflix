package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Playlist is a list of items a user made.
type Playlist struct {
	ID      string
	UserID  string
	Name    string
	Created time.Time
	Updated time.Time
	Entries []PlaylistEntry // in order
}

// PlaylistEntry is one place in a playlist.
type PlaylistEntry struct {
	ID     string
	ItemID string
}

// CreatePlaylist makes a playlist of a user with the given items.
func (s *Store) CreatePlaylist(ctx context.Context, userID, name string, itemIDs []string) (Playlist, error) {
	id, err := NewID()
	if err != nil {
		return Playlist{}, err
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, "INSERT INTO playlists(id, user_id, name, created, updated) VALUES(?, ?, ?, ?, ?)",
		id, userID, name, now.UnixMilli(), now.UnixMilli()); err != nil {
		return Playlist{}, err
	}
	if err := s.AddToPlaylist(ctx, id, itemIDs); err != nil {
		return Playlist{}, err
	}
	return s.Playlist(ctx, id)
}

func scanPlaylist(row interface{ Scan(...any) error }) (Playlist, error) {
	var p Playlist
	var created, updated int64
	if err := row.Scan(&p.ID, &p.UserID, &p.Name, &created, &updated); err != nil {
		return Playlist{}, err
	}
	p.Created, p.Updated = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return p, nil
}

func (s *Store) entries(ctx context.Context, playlistID string) ([]PlaylistEntry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, item_id FROM playlist_entries WHERE playlist_id = ? ORDER BY position, id",
		playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaylistEntry
	for rows.Next() {
		var e PlaylistEntry
		if err := rows.Scan(&e.ID, &e.ItemID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Playlist returns a playlist with its entries, ErrNotFound if there is
// none.
func (s *Store) Playlist(ctx context.Context, id string) (Playlist, error) {
	p, err := scanPlaylist(s.db.QueryRowContext(ctx, "SELECT id, user_id, name, created, updated FROM playlists WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Playlist{}, ErrNotFound
	}
	if err != nil {
		return Playlist{}, err
	}
	p.Entries, err = s.entries(ctx, id)
	return p, err
}

// Playlists returns the playlists of a user with their entries, by name.
func (s *Store) Playlists(ctx context.Context, userID string) ([]Playlist, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, user_id, name, created, updated FROM playlists WHERE user_id = ? ORDER BY name COLLATE NOCASE, id", userID)
	if err != nil {
		return nil, err
	}
	var out []Playlist
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Entries, err = s.entries(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) touchPlaylist(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "UPDATE playlists SET updated = ? WHERE id = ?", time.Now().UTC().UnixMilli(), id)
	return err
}

// AddToPlaylist appends items to a playlist.
func (s *Store) AddToPlaylist(ctx context.Context, id string, itemIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position) + 1, 0) FROM playlist_entries WHERE playlist_id = ?", id).
		Scan(&next); err != nil {
		return err
	}
	for _, item := range itemIDs {
		eid, err := NewID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO playlist_entries(id, playlist_id, item_id, position) VALUES(?, ?, ?, ?)",
			eid, id, item, next); err != nil {
			return err
		}
		next++
	}
	if err := s.touchPlaylist(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveFromPlaylist removes entries by entry ID.
func (s *Store) RemoveFromPlaylist(ctx context.Context, id string, entryIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range entryIDs {
		if _, err := tx.ExecContext(ctx, "DELETE FROM playlist_entries WHERE playlist_id = ? AND id = ?", id, e); err != nil {
			return err
		}
	}
	if err := s.touchPlaylist(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MovePlaylistEntry moves an entry to index (0 is the first place).
func (s *Store) MovePlaylistEntry(ctx context.Context, id, entryID string, index int) error {
	entries, err := s.entries(ctx, id)
	if err != nil {
		return err
	}
	from := -1
	for i, e := range entries {
		if e.ID == entryID {
			from = i
		}
	}
	if from < 0 {
		return ErrNotFound
	}
	e := entries[from]
	entries = append(entries[:from], entries[from+1:]...)
	index = min(max(index, 0), len(entries))
	entries = append(entries[:index], append([]PlaylistEntry{e}, entries[index:]...)...)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, e := range entries {
		if _, err := tx.ExecContext(ctx, "UPDATE playlist_entries SET position = ? WHERE id = ?", i, e.ID); err != nil {
			return err
		}
	}
	if err := s.touchPlaylist(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RenamePlaylist changes the name of a playlist.
func (s *Store) RenamePlaylist(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE playlists SET name = ?, updated = ? WHERE id = ?", name, time.Now().UTC().UnixMilli(), id)
	return err
}

// DeletePlaylist deletes a playlist with its entries.
func (s *Store) DeletePlaylist(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM playlists WHERE id = ?", id)
	return err
}

// Bookmarks returns what a client keeps of a kind ("bookmarks", "notes")
// for an item of a user, "" if nothing.
func (s *Store) Bookmarks(ctx context.Context, userID, itemID, kind string) (string, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM bookmarks WHERE user_id = ? AND item_id = ? AND kind = ?",
		userID, itemID, kind).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return data, err
}

// SetBookmarks stores what a client keeps of a kind for an item of a user.
func (s *Store) SetBookmarks(ctx context.Context, userID, itemID, kind, data string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO bookmarks(user_id, item_id, kind, data, updated) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(user_id, item_id, kind) DO UPDATE SET data = excluded.data, updated = excluded.updated`,
		userID, itemID, kind, data, time.Now().UTC().UnixMilli())
	return err
}
