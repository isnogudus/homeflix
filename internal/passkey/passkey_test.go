package passkey

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"homeflix/internal/passkey/passkeytest"
	"homeflix/internal/store"
)

var b64 = base64.RawURLEncoding

const (
	tailnet = "https://homeflix.example.ts.net"
	caddy   = "https://kino.example.de"
)

func setup(t *testing.T) (*Service, store.User) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.CreateUser(context.Background(), "anna", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st, "homeflix", []string{tailnet, caddy})
	if err != nil {
		t.Fatal(err)
	}
	return s, u
}

func TestRegisterAndSignIn(t *testing.T) {
	ctx := context.Background()
	s, anna := setup(t)
	if !s.Offered(tailnet) || s.Offered("https://other.example") || s.RPID(caddy) != "kino.example.de" {
		t.Fatal("offered origins")
	}
	if _, err := s.BeginRegistration(ctx, "https://other.example", anna); !errors.Is(err, ErrNotOffered) {
		t.Errorf("other origin: %v", err)
	}
	a := passkeytest.New(t)
	opts, err := s.BeginRegistration(ctx, tailnet, anna)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.FinishRegistration(ctx, tailnet, opts.Ceremony, anna, "iPhone", a.Create(opts.PublicKey, tailnet))
	if err != nil {
		t.Fatal(err)
	}
	if p.RPID != "homeflix.example.ts.net" || p.Name != "iPhone" {
		t.Errorf("stored passkey: %+v", p)
	}
	// A ceremony is used once.
	if _, err := s.FinishRegistration(ctx, tailnet, opts.Ceremony, anna, "again", a.Create(opts.PublicKey, tailnet)); !errors.Is(err, ErrCeremony) {
		t.Errorf("ceremony reused: %v", err)
	}
	// Registering again excludes the existing passkey.
	again, err := s.BeginRegistration(ctx, tailnet, anna)
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Exclude []struct{ ID string } `json:"excludeCredentials"`
	}
	json.Unmarshal(again.PublicKey, &reg)
	if len(reg.Exclude) != 1 || reg.Exclude[0].ID != b64.EncodeToString(a.ID) {
		t.Errorf("exclusions: %s", again.PublicKey)
	}

	login, err := s.BeginLogin(tailnet, true)
	if err != nil {
		t.Fatal(err)
	}
	if login.Mediation != "conditional" {
		t.Errorf("mediation %q", login.Mediation)
	}
	u, err := s.FinishLogin(ctx, tailnet, login.Ceremony, a.Get(login.PublicKey, tailnet))
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != anna.ID {
		t.Errorf("signed in %+v", u)
	}
	keys, _ := s.Store.Passkeys(ctx, anna.ID)
	if len(keys) != 1 || keys[0].LastUsedAt.IsZero() {
		t.Errorf("use not recorded: %+v", keys)
	}
	// A second sign-in works with the stored counter.
	login, _ = s.BeginLogin(tailnet, false)
	if _, err := s.FinishLogin(ctx, tailnet, login.Ceremony, a.Get(login.PublicKey, tailnet)); err != nil {
		t.Errorf("second sign-in: %v", err)
	}
}

func TestSignInRefusals(t *testing.T) {
	ctx := context.Background()
	s, anna := setup(t)
	a := passkeytest.New(t)
	opts, _ := s.BeginRegistration(ctx, tailnet, anna)
	if _, err := s.FinishRegistration(ctx, tailnet, opts.Ceremony, anna, "iPhone", a.Create(opts.PublicKey, tailnet)); err != nil {
		t.Fatal(err)
	}

	// The passkey of the tailnet name does not work through Caddy, even if
	// the browser answered for the right relying party.
	login, _ := s.BeginLogin(caddy, false)
	if _, err := s.FinishLogin(ctx, caddy, login.Ceremony, a.Get(login.PublicKey, caddy)); !errors.Is(err, ErrRejected) {
		t.Errorf("passkey of another relying party: %v", err)
	}
	// An answer signed for another origin.
	login, _ = s.BeginLogin(tailnet, false)
	if _, err := s.FinishLogin(ctx, tailnet, login.Ceremony, a.Get(login.PublicKey, "https://evil.example")); !errors.Is(err, ErrRejected) {
		t.Errorf("other origin in client data: %v", err)
	}
	// A ceremony started on one origin cannot be finished on another.
	login, _ = s.BeginLogin(tailnet, false)
	if _, err := s.FinishLogin(ctx, caddy, login.Ceremony, a.Get(login.PublicKey, tailnet)); !errors.Is(err, ErrCeremony) {
		t.Errorf("ceremony of another origin: %v", err)
	}
	// A copied passkey with a counter that went back.
	login, _ = s.BeginLogin(tailnet, false)
	if _, err := s.FinishLogin(ctx, tailnet, login.Ceremony, a.Get(login.PublicKey, tailnet)); err != nil {
		t.Fatal(err)
	}
	a.Count = 0 // the next answer repeats the counter of the last one
	login, _ = s.BeginLogin(tailnet, false)
	if _, err := s.FinishLogin(ctx, tailnet, login.Ceremony, a.Get(login.PublicKey, tailnet)); !errors.Is(err, ErrRejected) {
		t.Errorf("counter went back: %v", err)
	}
	// Expired ceremonies.
	now := time.Now()
	s.Now = func() time.Time { return now }
	login, _ = s.BeginLogin(tailnet, false)
	now = now.Add(ceremonyLifetime)
	if _, err := s.FinishLogin(ctx, tailnet, login.Ceremony, a.Get(login.PublicKey, tailnet)); !errors.Is(err, ErrCeremony) {
		t.Errorf("expired ceremony: %v", err)
	}
	// Removed passkeys no longer sign in.
	s.Now = nil
	if err := s.Store.DeletePasskey(ctx, anna.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	login, _ = s.BeginLogin(tailnet, false)
	if _, err := s.FinishLogin(ctx, tailnet, login.Ceremony, a.Get(login.PublicKey, tailnet)); !errors.Is(err, ErrRejected) {
		t.Errorf("removed passkey: %v", err)
	}
}
