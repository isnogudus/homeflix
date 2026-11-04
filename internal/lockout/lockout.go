// Package lockout slows down password guessing: a key, such as a user name
// or a client address, that fails too often within a window is refused for
// a while.
package lockout

import (
	"strings"
	"sync"
	"time"
)

// maxKeys bounds the memory used; keys without recent failures are dropped
// first.
const maxKeys = 10000

// Limiter counts failures per key.
type Limiter struct {
	Max    int           // failures within Window that block a key
	Window time.Duration // how long a failure counts
	Block  time.Duration // how long a key stays blocked
	Now    func() time.Time

	mu   sync.Mutex
	keys map[string]*entry
}

type entry struct {
	failures []time.Time
	until    time.Time
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Blocked reports whether key is blocked, and for how much longer.
func (l *Limiter) Blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.keys[key]
	if !ok {
		return 0, false
	}
	if left := e.until.Sub(l.now()); left > 0 {
		return left, true
	}
	return 0, false
}

// Fail records a failure of key; it blocks the key once Max failures fall
// within Window.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.keys == nil {
		l.keys = map[string]*entry{}
	}
	if len(l.keys) >= maxKeys {
		l.prune(now)
	}
	e, ok := l.keys[key]
	if !ok {
		e = &entry{}
		l.keys[key] = e
	}
	e.failures = append(recent(e.failures, now.Add(-l.Window)), now)
	if len(e.failures) >= l.Max {
		e.until = now.Add(l.Block)
		e.failures = nil
	}
}

// Reset forgets the failures of key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, key)
}

func recent(ts []time.Time, since time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// prune drops keys that are neither blocked nor have failures in the
// window; if that frees nothing, it drops all unblocked keys.
func (l *Limiter) prune(now time.Time) {
	for k, e := range l.keys {
		if !e.until.After(now) && len(recent(e.failures, now.Add(-l.Window))) == 0 {
			delete(l.keys, k)
		}
	}
	if len(l.keys) < maxKeys {
		return
	}
	for k, e := range l.keys {
		if !e.until.After(now) {
			delete(l.keys, k)
		}
	}
}

// Guard applies one limit per client address and a higher one per user
// name to logins. A success clears the user's failures but not the
// address's, so that a working account cannot be used to keep guessing
// others.
type Guard struct {
	ByAddr, ByUser *Limiter
}

// NewGuard allows 10 failures per address and 20 per user within 15
// minutes, then refuses for 15 minutes.
func NewGuard() *Guard {
	const window = 15 * time.Minute
	return &Guard{
		ByAddr: &Limiter{Max: 10, Window: window, Block: window},
		ByUser: &Limiter{Max: 20, Window: window, Block: window},
	}
}

func userKey(name string) string { return "user:" + foldCase(name) }

// Check reports whether a login of user from addr may be tried, and else
// how long to wait.
func (g *Guard) Check(user, addr string) (time.Duration, bool) {
	if left, blocked := g.ByAddr.Blocked(addr); blocked {
		return left, false
	}
	if left, blocked := g.ByUser.Blocked(userKey(user)); blocked {
		return left, false
	}
	return 0, true
}

// Failed records a failed login.
func (g *Guard) Failed(user, addr string) {
	g.ByAddr.Fail(addr)
	g.ByUser.Fail(userKey(user))
}

// Succeeded records a successful login.
func (g *Guard) Succeeded(user string) { g.ByUser.Reset(userKey(user)) }

// foldCase matches user names as the store does, regardless of case.
func foldCase(s string) string { return strings.ToLower(s) }
