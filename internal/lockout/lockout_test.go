package lockout

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := &Limiter{Max: 3, Window: 15 * time.Minute, Block: 15 * time.Minute, Now: func() time.Time { return now }}
	l.Fail("a")
	l.Fail("a")
	if _, blocked := l.Blocked("a"); blocked {
		t.Fatal("blocked after two failures")
	}
	// A failure outside the window does not count.
	now = now.Add(16 * time.Minute)
	l.Fail("a")
	l.Fail("a")
	if _, blocked := l.Blocked("a"); blocked {
		t.Fatal("old failures counted")
	}
	l.Fail("a")
	if left, blocked := l.Blocked("a"); !blocked || left != 15*time.Minute {
		t.Fatalf("not blocked after three failures: %v %v", left, blocked)
	}
	if _, blocked := l.Blocked("b"); blocked {
		t.Error("other key blocked")
	}
	now = now.Add(15 * time.Minute)
	if _, blocked := l.Blocked("a"); blocked {
		t.Error("still blocked after the block time")
	}
	l.Fail("a")
	l.Reset("a")
	l.Fail("a")
	l.Fail("a")
	if _, blocked := l.Blocked("a"); blocked {
		t.Error("reset did not clear the failures")
	}
}

func TestLimiterIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := &Limiter{Max: 1, Window: time.Minute, Block: time.Minute, Now: func() time.Time { return now }}
	for i := range maxKeys + 10 {
		l.Fail(fmt.Sprint("addr", i))
		now = now.Add(time.Second)
	}
	if len(l.keys) > maxKeys {
		t.Errorf("%d keys kept", len(l.keys))
	}
	if _, blocked := l.Blocked(fmt.Sprint("addr", maxKeys+9)); !blocked {
		t.Error("latest key lost")
	}
}

func TestGuard(t *testing.T) {
	g := NewGuard()
	for range 10 {
		g.Failed("anna", "203.0.113.9")
	}
	if _, ok := g.Check("bert", "203.0.113.9"); ok {
		t.Error("address not blocked after 10 failures")
	}
	if _, ok := g.Check("Anna", "198.51.100.1"); !ok {
		t.Error("user blocked after 10 failures, want 20")
	}
	// A success clears the user, not the address.
	g.Succeeded("ANNA")
	if _, ok := g.Check("anna", "203.0.113.9"); ok {
		t.Error("success cleared the address")
	}
	for range 20 {
		g.Failed("bert", fmt.Sprint("198.51.100.", 1))
	}
	if _, ok := g.Check("Bert", "192.0.2.7"); ok {
		t.Error("user not blocked after 20 failures from anywhere")
	}
}
