// Package passkey registers passkeys (WebAuthn credentials) and signs users
// in with them. Each configured origin is its own relying party, named by
// the origin's host: a passkey made on one address works only there.
// Verification is left to github.com/go-webauthn/webauthn.
package passkey

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"homeflix/internal/store"
)

// ceremonyLifetime is how long a started registration or sign-in may take.
const ceremonyLifetime = 5 * time.Minute

// maxCeremonies bounds the started ceremonies kept in memory.
const maxCeremonies = 1000

var (
	// ErrNotOffered means passkeys are not configured for the origin.
	ErrNotOffered = errors.New("passkeys are not offered on this address")
	// ErrCeremony means the ceremony is unknown, expired, or was started
	// on another address or for another user.
	ErrCeremony = errors.New("unknown or expired passkey request")
	// ErrRejected means the browser's answer did not verify.
	ErrRejected = errors.New("passkey not accepted")
	// ErrBusy means too many ceremonies are open.
	ErrBusy = errors.New("too many passkey requests")
)

// Service holds the relying parties and the ceremonies in progress.
type Service struct {
	Store *store.Store
	// Now returns the time; time.Now if nil.
	Now func() time.Time

	rps     map[string]*webauthn.WebAuthn // by origin
	origins []string

	mu         sync.Mutex
	ceremonies map[string]ceremony
}

type ceremony struct {
	origin  string
	userID  string // for registrations
	data    webauthn.SessionData
	expires time.Time
}

// New makes a service for the given origins, such as
// "https://homeflix.example.ts.net". name is shown by authenticators.
func New(st *store.Store, name string, origins []string) (*Service, error) {
	s := &Service{Store: st, rps: map[string]*webauthn.WebAuthn{}, origins: origins}
	for _, o := range origins {
		u, err := url.Parse(o)
		if err != nil {
			return nil, err
		}
		rp, err := webauthn.New(&webauthn.Config{
			RPID:          u.Hostname(),
			RPDisplayName: name,
			RPOrigins:     []string{o},
			AuthenticatorSelection: protocol.AuthenticatorSelection{
				ResidentKey:      protocol.ResidentKeyRequirementRequired,
				UserVerification: protocol.VerificationRequired,
			},
			AttestationPreference: protocol.PreferNoAttestation,
			Timeouts: webauthn.TimeoutsConfig{
				Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyLifetime, TimeoutUVD: ceremonyLifetime},
				Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyLifetime, TimeoutUVD: ceremonyLifetime},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("passkeys for %s: %w", o, err)
		}
		s.rps[o] = rp
	}
	return s, nil
}

// Offered reports whether passkeys work on origin.
func (s *Service) Offered(origin string) bool {
	if s == nil {
		return false
	}
	_, ok := s.rps[origin]
	return ok
}

// Origins are the origins passkeys are offered on.
func (s *Service) Origins() []string {
	if s == nil {
		return nil
	}
	return s.origins
}

// RPID is the relying party of origin, the host passkeys are bound to.
func (s *Service) RPID(origin string) string {
	if !s.Offered(origin) {
		return ""
	}
	return s.rps[origin].Config.RPID
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// account is a user as the WebAuthn library sees it, with the passkeys of
// one relying party.
type account struct {
	user  store.User
	creds []webauthn.Credential
}

func (a *account) WebAuthnID() []byte                         { return []byte(a.user.ID) }
func (a *account) WebAuthnName() string                       { return a.user.Name }
func (a *account) WebAuthnDisplayName() string                { return a.user.Name }
func (a *account) WebAuthnCredentials() []webauthn.Credential { return a.creds }

func (s *Service) account(ctx context.Context, user store.User, rpID string) (*account, error) {
	keys, err := s.Store.Passkeys(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	a := &account{user: user}
	for _, k := range keys {
		if k.RPID != rpID {
			continue
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(k.Data), &c); err != nil {
			return nil, fmt.Errorf("passkey %x: %w", k.CredentialID, err)
		}
		a.creds = append(a.creds, c)
	}
	return a, nil
}

// remember keeps a started ceremony and returns its ID.
func (s *Service) remember(c ceremony) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, v := range s.ceremonies {
		if !now.Before(v.expires) {
			delete(s.ceremonies, k)
		}
	}
	if s.ceremonies == nil {
		s.ceremonies = map[string]ceremony{}
	}
	if len(s.ceremonies) >= maxCeremonies {
		return "", ErrBusy
	}
	c.expires = now.Add(ceremonyLifetime)
	s.ceremonies[id] = c
	return id, nil
}

// take hands out a ceremony once.
func (s *Service) take(id, origin, userID string) (ceremony, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ceremonies[id]
	delete(s.ceremonies, id)
	if !ok || !s.now().Before(c.expires) || c.origin != origin || c.userID != userID {
		return ceremony{}, ErrCeremony
	}
	return c, nil
}

// Options are what the browser passes to navigator.credentials, as JSON,
// and the ceremony ID to send back with its answer.
type Options struct {
	Ceremony  string          `json:"ceremony"`
	PublicKey json.RawMessage `json:"publicKey"`
	Mediation string          `json:"mediation,omitempty"`
}

// BeginRegistration starts adding a passkey for user on origin.
func (s *Service) BeginRegistration(ctx context.Context, origin string, user store.User) (Options, error) {
	rp, ok := s.rps[origin]
	if !ok {
		return Options{}, ErrNotOffered
	}
	a, err := s.account(ctx, user, rp.Config.RPID)
	if err != nil {
		return Options{}, err
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range a.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, data, err := rp.BeginRegistration(a, webauthn.WithExclusions(exclude))
	if err != nil {
		return Options{}, err
	}
	return s.options(ceremony{origin: origin, userID: user.ID, data: *data}, creation.Response, "")
}

func (s *Service) options(c ceremony, publicKey any, mediation string) (Options, error) {
	pk, err := json.Marshal(publicKey)
	if err != nil {
		return Options{}, err
	}
	id, err := s.remember(c)
	if err != nil {
		return Options{}, err
	}
	return Options{Ceremony: id, PublicKey: pk, Mediation: mediation}, nil
}

// FinishRegistration verifies the browser's answer and stores the passkey
// under name.
func (s *Service) FinishRegistration(ctx context.Context, origin, ceremonyID string, user store.User, name string,
	response []byte) (store.Passkey, error) {
	c, err := s.take(ceremonyID, origin, user.ID)
	if err != nil {
		return store.Passkey{}, err
	}
	rp := s.rps[origin]
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return store.Passkey{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	a, err := s.account(ctx, user, rp.Config.RPID)
	if err != nil {
		return store.Passkey{}, err
	}
	cred, err := rp.CreateCredential(a, c.data, parsed)
	if err != nil {
		return store.Passkey{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return store.Passkey{}, err
	}
	p := store.Passkey{CredentialID: cred.ID, UserID: user.ID, RPID: rp.Config.RPID, Name: name, Data: string(data),
		CreatedAt: s.now().UTC()}
	if err := s.Store.CreatePasskey(ctx, p); err != nil {
		return store.Passkey{}, err
	}
	return p, nil
}

// BeginLogin starts a sign-in on origin with any passkey of the relying
// party; the browser lets the user pick one. With conditional, browsers
// offer the passkeys in the user name field's autofill.
func (s *Service) BeginLogin(origin string, conditional bool) (Options, error) {
	rp, ok := s.rps[origin]
	if !ok {
		return Options{}, ErrNotOffered
	}
	mediation := protocol.MediationDefault
	if conditional {
		mediation = protocol.MediationConditional
	}
	assertion, data, err := rp.BeginDiscoverableMediatedLogin(mediation)
	if err != nil {
		return Options{}, err
	}
	return s.options(ceremony{origin: origin, data: *data}, assertion.Response, string(mediation))
}

// FinishLogin verifies the browser's answer and returns the user it signs
// in, recording the use of the passkey.
func (s *Service) FinishLogin(ctx context.Context, origin, ceremonyID string, response []byte) (store.User, error) {
	c, err := s.take(ceremonyID, origin, "")
	if err != nil {
		return store.User{}, err
	}
	rp := s.rps[origin]
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return store.User{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	var found *account
	lookup := func(rawID, userHandle []byte) (webauthn.User, error) {
		p, err := s.Store.Passkey(ctx, rawID)
		if err != nil {
			return nil, err
		}
		if p.UserID != string(userHandle) || p.RPID != rp.Config.RPID {
			return nil, errors.New("passkey belongs to another user or address")
		}
		u, err := s.Store.UserByID(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		if found, err = s.account(ctx, u, rp.Config.RPID); err != nil {
			return nil, err
		}
		return found, nil
	}
	_, cred, err := rp.ValidatePasskeyLogin(lookup, c.data, parsed)
	if err != nil {
		return store.User{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	if cred.Authenticator.CloneWarning {
		return store.User{}, fmt.Errorf("%w: the signature counter went back; the passkey may be copied", ErrRejected)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return store.User{}, err
	}
	if err := s.Store.UsePasskey(ctx, cred.ID, string(data), s.now().UTC()); err != nil {
		return store.User{}, err
	}
	return found.user, nil
}
