package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UserData is what one user has done with one item.
type UserData struct {
	ItemID     string
	Played     bool
	PlayCount  int
	Position   time.Duration // resume position; 0 if none
	LastPlayed time.Time     // zero if never
	IsFavorite bool
}

// IsZero reports whether d holds no state at all.
func (d UserData) IsZero() bool {
	return !d.Played && d.PlayCount == 0 && d.Position == 0 && d.LastPlayed.IsZero() && !d.IsFavorite
}

const userDataColumns = "item_id, played, play_count, position_ms, last_played, is_favorite"

func scanUserData(row interface{ Scan(...any) error }) (UserData, error) {
	var d UserData
	var pos int64
	var last sql.NullInt64
	if err := row.Scan(&d.ItemID, &d.Played, &d.PlayCount, &pos, &last, &d.IsFavorite); err != nil {
		return UserData{}, err
	}
	d.Position = time.Duration(pos) * time.Millisecond
	d.LastPlayed = fromMillis(last)
	return d, nil
}

// AllUserData returns the state of every item the user has touched.
func (s *Store) AllUserData(ctx context.Context, userID string) (map[string]UserData, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userDataColumns+" FROM user_data WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]UserData{}
	for rows.Next() {
		d, err := scanUserData(rows)
		if err != nil {
			return nil, err
		}
		out[d.ItemID] = d
	}
	return out, rows.Err()
}

// GetUserData returns the user's state of an item; a zero value with the
// item ID set if there is none.
func (s *Store) GetUserData(ctx context.Context, userID, itemID string) (UserData, error) {
	d, err := scanUserData(s.db.QueryRowContext(ctx,
		"SELECT "+userDataColumns+" FROM user_data WHERE user_id = ? AND item_id = ?", userID, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return UserData{ItemID: itemID}, nil
	}
	return d, err
}

// SaveUserData stores the state of an item, removing the row when nothing
// is left to remember.
func (s *Store) SaveUserData(ctx context.Context, userID string, d UserData) error {
	if d.IsZero() {
		_, err := s.db.ExecContext(ctx, "DELETE FROM user_data WHERE user_id = ? AND item_id = ?", userID, d.ItemID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_data(user_id, `+userDataColumns+`) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, item_id) DO UPDATE SET played = excluded.played, play_count = excluded.play_count,
		position_ms = excluded.position_ms, last_played = excluded.last_played, is_favorite = excluded.is_favorite`,
		userID, d.ItemID, d.Played, d.PlayCount, d.Position.Milliseconds(), toMillis(d.LastPlayed), d.IsFavorite)
	return err
}
