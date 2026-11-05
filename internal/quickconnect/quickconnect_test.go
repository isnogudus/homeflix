package quickconnect

import (
	"errors"
	"testing"
	"time"
)

func TestFlow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &Service{Now: func() time.Time { return now }}
	r, err := s.Initiate(Request{DeviceID: "d1", DeviceName: "iMac", AppName: "Moonfin", AppVersion: "1.0", Remote: "192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Code) != 6 || r.Code[0] == '0' || len(r.Secret) != 64 || r.Authenticated() {
		t.Fatalf("new request: %+v", r)
	}
	if _, err := s.Redeem(r.Secret); !errors.Is(err, ErrUnknown) {
		t.Errorf("redeem before authorize: %v", err)
	}
	if _, err := s.Authorize("000000", "u1"); !errors.Is(err, ErrUnknown) {
		t.Errorf("wrong code: %v", err)
	}
	if _, err := s.Authorize(" "+r.Code+" ", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authorize(r.Code, "u2"); !errors.Is(err, ErrAuthorized) {
		t.Errorf("second authorize: %v", err)
	}
	st, err := s.Status(r.Secret)
	if err != nil || !st.Authenticated() || st.UserID != "u1" {
		t.Errorf("status: %+v %v", st, err)
	}
	got, err := s.Redeem(r.Secret)
	if err != nil || got.UserID != "u1" || got.DeviceID != "d1" || got.AppName != "Moonfin" {
		t.Errorf("redeem: %+v %v", got, err)
	}
	if _, err := s.Redeem(r.Secret); !errors.Is(err, ErrUnknown) {
		t.Errorf("second redeem: %v", err)
	}
}

func TestExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &Service{Now: func() time.Time { return now }}
	r, _ := s.Initiate(Request{Remote: "a"})
	now = now.Add(Lifetime - time.Second)
	if _, err := s.Authorize(r.Code, "u1"); err != nil {
		t.Fatal(err)
	}
	// Confirmed requests get a fresh lifetime to be collected.
	now = now.Add(Lifetime - time.Second)
	if _, err := s.Status(r.Secret); err != nil {
		t.Errorf("confirmed request expired early: %v", err)
	}
	now = now.Add(time.Second)
	if _, err := s.Redeem(r.Secret); !errors.Is(err, ErrUnknown) {
		t.Errorf("expired request redeemed: %v", err)
	}
	r2, _ := s.Initiate(Request{Remote: "a"})
	now = now.Add(Lifetime)
	if _, err := s.Authorize(r2.Code, "u1"); !errors.Is(err, ErrUnknown) {
		t.Errorf("expired code authorized: %v", err)
	}
}

func TestLimits(t *testing.T) {
	s := &Service{}
	for i := range maxPendingPerAddr {
		if _, err := s.Initiate(Request{Remote: "a"}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := s.Initiate(Request{Remote: "a"}); !errors.Is(err, ErrTooMany) {
		t.Errorf("over the per-address limit: %v", err)
	}
	if _, err := s.Initiate(Request{Remote: "b"}); err != nil {
		t.Errorf("other address: %v", err)
	}
	if s.Pending() != maxPendingPerAddr+1 {
		t.Errorf("pending %d", s.Pending())
	}
	if formatCode(100042) != "100042" || formatCode(999999) != "999999" {
		t.Error("formatCode")
	}
}

func TestWrongCodes(t *testing.T) {
	s := &Service{}
	r, _ := s.Initiate(Request{Remote: "a"})
	for range maxGuesses {
		if _, err := s.Authorize("000000", "u1"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	if _, err := s.Authorize(r.Code, "u1"); !errors.Is(err, ErrTooMany) {
		t.Errorf("after too many wrong codes: %v", err)
	}
	if _, err := s.Authorize(r.Code, "u2"); err != nil {
		t.Errorf("another user: %v", err)
	}
}
