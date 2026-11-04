package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned when a looked-up row does not exist.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when a row with the same unique key exists.
var ErrExists = errors.New("already exists")

// User is a server account.
type User struct {
	ID             string
	Name           string
	PasswordHash   string
	IsAdmin        bool
	Configuration  string // JSON object as last sent by a client
	CreatedAt      time.Time
	LastLoginAt    time.Time // zero if never
	LastActivityAt time.Time // zero if never
}

// Token is an access token issued to one device of one user. The token
// itself is never stored, only its hash.
type Token struct {
	Hash          string
	SessionID     string
	UserID        string
	DeviceID      string
	DeviceName    string
	Client        string
	ClientVersion string
	RemoteAddr    string
	CreatedAt     time.Time
	LastSeenAt    time.Time
}

func toMillis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func fromMillis(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

const userColumns = "id, name, password_hash, is_admin, configuration, created_at, last_login_at, last_activity_at"

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created, login, activity sql.NullInt64
	err := row.Scan(&u.ID, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.Configuration, &created, &login, &activity)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt, u.LastLoginAt, u.LastActivityAt = fromMillis(created), fromMillis(login), fromMillis(activity)
	return u, nil
}

// CreateUser adds a user. Names are unique regardless of case.
func (s *Store) CreateUser(ctx context.Context, name, passwordHash string, isAdmin bool) (User, error) {
	id, err := NewID()
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO users(id, name, password_hash, is_admin, created_at) VALUES(?, ?, ?, ?, ?)",
		id, name, passwordHash, isAdmin, now.UnixMilli())
	if isUniqueViolation(err) {
		return User{}, fmt.Errorf("user %q: %w", name, ErrExists)
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, id)
}

// UserByID looks a user up by ID.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id))
}

// UserByName looks a user up by name, ignoring case.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE name = ?", name))
}

// Users lists all users ordered by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func expectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPassword replaces the password hash and revokes all tokens of the user.
func (s *Store) SetPassword(ctx context.Context, userID, passwordHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := expectOne(tx.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", passwordHash, userID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tokens WHERE user_id = ?", userID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAdmin grants or removes administrator rights.
func (s *Store) SetAdmin(ctx context.Context, userID string, admin bool) error {
	return expectOne(s.db.ExecContext(ctx, "UPDATE users SET is_admin = ? WHERE id = ?", admin, userID))
}

// DeleteUser removes a user together with its tokens and preferences.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	return expectOne(s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", userID))
}

// SetUserConfiguration stores the client-visible user configuration (JSON).
func (s *Store) SetUserConfiguration(ctx context.Context, userID, configJSON string) error {
	return expectOne(s.db.ExecContext(ctx, "UPDATE users SET configuration = ? WHERE id = ?", configJSON, userID))
}

// CreateToken stores a new token, replacing any earlier token of the same
// user and device, and records the login time.
func (s *Store) CreateToken(ctx context.Context, t Token) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM tokens WHERE user_id = ? AND device_id = ?", t.UserID, t.DeviceID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tokens(token_hash, session_id, user_id, device_id, device_name,
		client, client_version, remote_addr, created_at, last_seen_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Hash, t.SessionID, t.UserID, t.DeviceID, t.DeviceName, t.Client, t.ClientVersion, t.RemoteAddr,
		t.CreatedAt.UnixMilli(), t.LastSeenAt.UnixMilli())
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET last_login_at = ?, last_activity_at = ? WHERE id = ?",
		t.CreatedAt.UnixMilli(), t.CreatedAt.UnixMilli(), t.UserID); err != nil {
		return err
	}
	return tx.Commit()
}

const tokenColumns = "t.token_hash, t.session_id, t.user_id, t.device_id, t.device_name, t.client, t.client_version, t.remote_addr, t.created_at, t.last_seen_at"

func scanToken(row interface{ Scan(...any) error }) (Token, error) {
	var t Token
	var created, seen sql.NullInt64
	if err := row.Scan(&t.Hash, &t.SessionID, &t.UserID, &t.DeviceID, &t.DeviceName, &t.Client,
		&t.ClientVersion, &t.RemoteAddr, &created, &seen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Token{}, ErrNotFound
		}
		return Token{}, err
	}
	t.CreatedAt, t.LastSeenAt = fromMillis(created), fromMillis(seen)
	return t, nil
}

// TokenByHash returns the token with the given hash and its user.
func (s *Store) TokenByHash(ctx context.Context, hash string) (Token, User, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+tokenColumns+" FROM tokens t WHERE t.token_hash = ?", hash)
	t, err := scanToken(row)
	if err != nil {
		return Token{}, User{}, err
	}
	u, err := s.UserByID(ctx, t.UserID)
	if err != nil {
		return Token{}, User{}, err
	}
	return t, u, nil
}

// TouchToken records activity on a token and its user.
func (s *Store) TouchToken(ctx context.Context, hash, remoteAddr string, at time.Time) error {
	ms := at.UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE tokens SET last_seen_at = ?, remote_addr = ? WHERE token_hash = ?",
		ms, remoteAddr, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE users SET last_activity_at = ? WHERE id = (SELECT user_id FROM tokens WHERE token_hash = ?)",
		ms, hash); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteToken revokes a token.
func (s *Store) DeleteToken(ctx context.Context, hash string) error {
	return expectOne(s.db.ExecContext(ctx, "DELETE FROM tokens WHERE token_hash = ?", hash))
}

// DeleteSession revokes the token of one session of a user.
func (s *Store) DeleteSession(ctx context.Context, userID, sessionID string) error {
	return expectOne(s.db.ExecContext(ctx, "DELETE FROM tokens WHERE user_id = ? AND session_id = ?", userID, sessionID))
}

// Tokens lists tokens, newest activity first. An empty userID lists all.
func (s *Store) Tokens(ctx context.Context, userID string) ([]Token, error) {
	q := "SELECT " + tokenColumns + " FROM tokens t"
	var args []any
	if userID != "" {
		q += " WHERE t.user_id = ?"
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY t.last_seen_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// DisplayPrefs returns the stored display preferences JSON.
func (s *Store) DisplayPrefs(ctx context.Context, userID, prefID, client string) (string, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		"SELECT data FROM display_prefs WHERE user_id = ? AND pref_id = ? AND client = ?",
		userID, prefID, client).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return data, err
}

// SetDisplayPrefs stores display preferences JSON.
func (s *Store) SetDisplayPrefs(ctx context.Context, userID, prefID, client, data string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO display_prefs(user_id, pref_id, client, data) VALUES(?, ?, ?, ?)
		ON CONFLICT(user_id, pref_id, client) DO UPDATE SET data = excluded.data`,
		userID, prefID, client, data)
	return err
}
