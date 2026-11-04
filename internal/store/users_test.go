package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUserLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())

	u, err := s.CreateUser(ctx, "Anna", "hash1", true)
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin || u.Configuration != "{}" || u.CreatedAt.IsZero() || !u.LastLoginAt.IsZero() {
		t.Errorf("unexpected new user: %+v", u)
	}
	if _, err := s.CreateUser(ctx, "anna", "hash2", false); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate name with other case: err %v, want ErrExists", err)
	}
	byName, err := s.UserByName(ctx, "ANNA")
	if err != nil || byName.ID != u.ID {
		t.Errorf("UserByName ignoring case: %+v, %v", byName, err)
	}
	if _, err := s.UserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: err %v", err)
	}
	if err := s.SetUserConfiguration(ctx, u.ID, `{"SubtitleMode":"Smart"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByID(ctx, u.ID); got.Configuration != `{"SubtitleMode":"Smart"}` {
		t.Errorf("configuration not stored: %q", got.Configuration)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err %v", err)
	}
}

func newToken(hash, userID, device string) Token {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Token{
		Hash: hash, SessionID: "s-" + hash, UserID: userID, DeviceID: device, DeviceName: "iMac",
		Client: "Moonfin", ClientVersion: "3.0.0", RemoteAddr: "192.168.1.20", CreatedAt: now, LastSeenAt: now,
	}
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	u, err := s.CreateUser(ctx, "anna", "h", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.CreateToken(ctx, newToken("t1", u.ID, "dev1")); err != nil {
		t.Fatal(err)
	}
	tok, owner, err := s.TokenByHash(ctx, "t1")
	if err != nil || owner.ID != u.ID || tok.Client != "Moonfin" {
		t.Fatalf("TokenByHash: %+v %+v %v", tok, owner, err)
	}
	if owner.LastLoginAt.IsZero() {
		t.Error("login time not recorded")
	}

	// A new login from the same device replaces the token.
	if err := s.CreateToken(ctx, newToken("t2", u.ID, "dev1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TokenByHash(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old token still valid: %v", err)
	}
	if err := s.CreateToken(ctx, newToken("t3", u.ID, "dev2")); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Tokens(ctx, ""); len(all) != 2 {
		t.Errorf("want 2 tokens, got %d", len(all))
	}

	later := time.Now().Add(time.Hour)
	if err := s.TouchToken(ctx, "t3", "10.0.0.1", later); err != nil {
		t.Fatal(err)
	}
	tok, owner, _ = s.TokenByHash(ctx, "t3")
	if tok.LastSeenAt.UnixMilli() != later.UnixMilli() || tok.RemoteAddr != "10.0.0.1" || owner.LastActivityAt.UnixMilli() != later.UnixMilli() {
		t.Errorf("touch not recorded: %+v %+v", tok, owner)
	}

	// Changing the password revokes every token of the user.
	if err := s.SetPassword(ctx, u.ID, "h2"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Tokens(ctx, u.ID); len(all) != 0 {
		t.Errorf("tokens survived password change: %d", len(all))
	}
}

func TestDisplayPrefs(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	u, _ := s.CreateUser(ctx, "anna", "h", false)
	if _, err := s.DisplayPrefs(ctx, u.ID, "usersettings", "moonfin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing prefs: err %v", err)
	}
	for _, v := range []string{`{"a":1}`, `{"a":2}`} {
		if err := s.SetDisplayPrefs(ctx, u.ID, "usersettings", "moonfin", v); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.DisplayPrefs(ctx, u.ID, "usersettings", "moonfin"); err != nil || got != `{"a":2}` {
		t.Errorf("DisplayPrefs = %q, %v", got, err)
	}
	// Prefs go away with the user.
	s.DeleteUser(ctx, u.ID)
	if _, err := s.DisplayPrefs(ctx, u.ID, "usersettings", "moonfin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("prefs survived user deletion: %v", err)
	}
}

func TestAdminFlagAndSessions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	u, err := s.CreateUser(ctx, "anna", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAdmin(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByID(ctx, u.ID); !got.IsAdmin {
		t.Error("admin flag not set")
	}
	if err := s.SetAdmin(ctx, "nobody", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"s1", "s2"} {
		if err := s.CreateToken(ctx, Token{Hash: "h" + id, SessionID: id, UserID: u.ID, DeviceID: "d" + id,
			DeviceName: "x", Client: "c", CreatedAt: now, LastSeenAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteSession(ctx, u.ID, "s1"); err != nil {
		t.Fatal(err)
	}
	if toks, _ := s.Tokens(ctx, u.ID); len(toks) != 1 || toks[0].SessionID != "s2" {
		t.Errorf("tokens after ending s1: %+v", toks)
	}
	if err := s.DeleteSession(ctx, "other", "s2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("session of another user: %v", err)
	}
}
