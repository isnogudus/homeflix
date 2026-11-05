// Package quickconnect lets an app sign in without a password: the app asks
// for a code and shows it, a user who is signed in elsewhere enters the code,
// and the app then gets a token of its own. It follows Jellyfin's Quick
// Connect: six-digit codes, a secret the app polls with, ten minutes to
// confirm.
package quickconnect

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"homeflix/internal/lockout"
)

// Lifetime is how long a code waits to be confirmed, and how long a
// confirmed request waits for the app to collect its token.
const Lifetime = 10 * time.Minute

// Limits on pending requests, so that anonymous apps cannot fill memory.
const (
	maxPending        = 200
	maxPendingPerAddr = 10
)

var (
	// ErrUnknown means no pending request has the code or secret, or it
	// has expired.
	ErrUnknown = errors.New("unknown or expired quick connect request")
	// ErrAuthorized means the code was confirmed already.
	ErrAuthorized = errors.New("quick connect request is already authorized")
	// ErrTooMany means too many requests are pending, or a user entered
	// too many wrong codes.
	ErrTooMany = errors.New("too many quick connect requests")
)

// Request is an app waiting to be signed in.
type Request struct {
	Secret     string // the app polls with it; never shown to users
	Code       string // shown by the app, entered by the user
	DeviceID   string
	DeviceName string
	AppName    string
	AppVersion string
	Remote     string // client address of the app
	Added      time.Time
	// UserID is set when a user confirmed the code.
	UserID     string
	Authorized time.Time
}

// Authenticated reports whether a user confirmed the code.
func (r Request) Authenticated() bool { return r.UserID != "" }

// Service holds the pending requests in memory.
type Service struct {
	// Now returns the time; time.Now if nil.
	Now func() time.Time

	mu       sync.Mutex
	bySecret map[string]*Request
	guesses  *lockout.Limiter // wrong codes per user
}

// Wrong codes a user may enter before having to wait.
const (
	maxGuesses  = 10
	guessWindow = 15 * time.Minute
)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// expire drops requests that waited too long. Callers hold s.mu.
func (s *Service) expire(now time.Time) {
	for k, r := range s.bySecret {
		since := r.Added
		if r.Authenticated() {
			since = r.Authorized
		}
		if now.Sub(since) >= Lifetime {
			delete(s.bySecret, k)
		}
	}
}

// Initiate registers an app waiting for a code; the device fields of r are
// kept, Secret, Code and Added are filled in.
func (s *Service) Initiate(r Request) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	if s.bySecret == nil {
		s.bySecret = map[string]*Request{}
	}
	perAddr := 0
	for _, p := range s.bySecret {
		if p.Remote == r.Remote {
			perAddr++
		}
	}
	if len(s.bySecret) >= maxPending || perAddr >= maxPendingPerAddr {
		return Request{}, ErrTooMany
	}
	secret, err := randomSecret()
	if err != nil {
		return Request{}, err
	}
	for {
		code, err := randomCode()
		if err != nil {
			return Request{}, err
		}
		if s.byCode(code) == nil {
			r.Code = code
			break
		}
	}
	r.Secret, r.Added, r.UserID, r.Authorized = secret, now, "", time.Time{}
	s.bySecret[secret] = &r
	return r, nil
}

// byCode finds a pending request by its code. Callers hold s.mu.
func (s *Service) byCode(code string) *Request {
	for _, r := range s.bySecret {
		if r.Code == code {
			return r
		}
	}
	return nil
}

// Status returns the request of a secret, for the app polling it.
func (s *Service) Status(secret string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(s.now())
	r, ok := s.bySecret[secret]
	if !ok {
		return Request{}, ErrUnknown
	}
	return *r, nil
}

// Authorize confirms the request with the code for a user.
func (s *Service) Authorize(code, userID string) (Request, error) {
	code = strings.TrimSpace(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	if s.guesses == nil {
		s.guesses = &lockout.Limiter{Max: maxGuesses, Window: guessWindow, Block: guessWindow, Now: s.now}
	}
	if _, blocked := s.guesses.Blocked(userID); blocked {
		return Request{}, ErrTooMany
	}
	r := s.byCode(code)
	switch {
	case r == nil || code == "":
		s.guesses.Fail(userID)
		return Request{}, ErrUnknown
	case r.Authenticated():
		return Request{}, ErrAuthorized
	}
	r.UserID, r.Authorized = userID, now
	return *r, nil
}

// Redeem hands out a confirmed request once, for the app to get its token.
func (s *Service) Redeem(secret string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(s.now())
	r, ok := s.bySecret[secret]
	if !ok || !r.Authenticated() {
		return Request{}, ErrUnknown
	}
	delete(s.bySecret, secret)
	return *r, nil
}

// Pending is the number of requests waiting, for the admin UI.
func (s *Service) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(s.now())
	return len(s.bySecret)
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}

// randomCode is a six-digit code without a leading zero, as in Jellyfin.
func randomCode() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	n := 100000 + binary.BigEndian.Uint64(b[:])%900000
	return formatCode(n), nil
}

func formatCode(n uint64) string {
	var d [6]byte
	for i := 5; i >= 0; i-- {
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[:])
}
