package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPasskeys(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	u, err := s.CreateUser(ctx, "Anna", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_800_000_000_000).UTC()
	p := Passkey{CredentialID: []byte{1, 2, 3}, UserID: u.ID, RPID: "homeflix.example.ts.net", Name: "Safari on iPhone",
		Data: `{"id":"AQID"}`, CreatedAt: now}
	if err := s.CreatePasskey(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePasskey(ctx, p); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate credential: %v", err)
	}
	got, err := s.Passkey(ctx, []byte{1, 2, 3})
	if err != nil || got.UserID != u.ID || got.RPID != p.RPID || !got.CreatedAt.Equal(now) || !got.LastUsedAt.IsZero() {
		t.Errorf("Passkey: %+v %v", got, err)
	}
	if err := s.UsePasskey(ctx, p.CredentialID, `{"id":"AQID","n":2}`, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, err := s.Passkeys(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].Data != `{"id":"AQID","n":2}` || !list[0].LastUsedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("Passkeys after use: %+v %v", list, err)
	}
	if err := s.DeletePasskey(ctx, "other", p.CredentialID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete by another user: %v", err)
	}
	if err := s.DeletePasskey(ctx, u.ID, p.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Passkey(ctx, p.CredentialID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted passkey: %v", err)
	}
	// Deleting the user removes its passkeys.
	if err := s.CreatePasskey(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Passkey(ctx, p.CredentialID); !errors.Is(err, ErrNotFound) {
		t.Errorf("passkey of deleted user: %v", err)
	}
}
