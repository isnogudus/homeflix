package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Passkey is a WebAuthn credential of a user. Data is the credential as
// the WebAuthn library stores it; the store does not look into it.
type Passkey struct {
	CredentialID []byte
	UserID       string
	RPID         string // relying party: the host it was registered on
	Name         string
	Data         string
	CreatedAt    time.Time
	LastUsedAt   time.Time // zero if never
}

const passkeyColumns = "credential_id, user_id, rp_id, name, data, created_at, last_used_at"

func scanPasskey(row interface{ Scan(...any) error }) (Passkey, error) {
	var p Passkey
	var created, used sql.NullInt64
	err := row.Scan(&p.CredentialID, &p.UserID, &p.RPID, &p.Name, &p.Data, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrNotFound
	}
	if err != nil {
		return Passkey{}, err
	}
	p.CreatedAt, p.LastUsedAt = fromMillis(created), fromMillis(used)
	return p, nil
}

// CreatePasskey stores a new passkey; ErrExists if its credential ID is
// known.
func (s *Store) CreatePasskey(ctx context.Context, p Passkey) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO passkeys ("+passkeyColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
		p.CredentialID, p.UserID, p.RPID, p.Name, p.Data, toMillis(p.CreatedAt), toMillis(p.LastUsedAt))
	if isUniqueViolation(err) {
		return ErrExists
	}
	return err
}

// Passkey returns the passkey with the given credential ID.
func (s *Store) Passkey(ctx context.Context, credentialID []byte) (Passkey, error) {
	return scanPasskey(s.db.QueryRowContext(ctx, "SELECT "+passkeyColumns+" FROM passkeys WHERE credential_id = ?",
		credentialID))
}

// Passkeys lists the passkeys of a user, oldest first.
func (s *Store) Passkeys(ctx context.Context, userID string) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+passkeyColumns+" FROM passkeys WHERE user_id = ? ORDER BY created_at",
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UsePasskey records a sign-in with a passkey and its updated data, such
// as the signature counter.
func (s *Store) UsePasskey(ctx context.Context, credentialID []byte, data string, at time.Time) error {
	return expectOne(s.db.ExecContext(ctx, "UPDATE passkeys SET data = ?, last_used_at = ? WHERE credential_id = ?",
		data, toMillis(at), credentialID))
}

// DeletePasskey removes a passkey of a user.
func (s *Store) DeletePasskey(ctx context.Context, userID string, credentialID []byte) error {
	return expectOne(s.db.ExecContext(ctx, "DELETE FROM passkeys WHERE user_id = ? AND credential_id = ?",
		userID, credentialID))
}
