package auth

import (
	"regexp"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("unexpected hash format %q", h)
	}
	for pw, want := range map[string]bool{"correct horse": true, "correct horse ": false, "": false} {
		ok, err := VerifyPassword(h, pw)
		if err != nil || ok != want {
			t.Errorf("VerifyPassword(%q) = %v, %v; want %v", pw, ok, err, want)
		}
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Error("two hashes of the same password are identical; salt missing")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AAAA$AAAA", "$argon2id$v=19$m=x$AAAA$AAAA"} {
		if ok, err := VerifyPassword(h, "pw"); ok || err == nil {
			t.Errorf("VerifyPassword(%q) = %v, %v; want error", h, ok, err)
		}
	}
}

func TestTokens(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a) || a == b {
		t.Errorf("tokens %q, %q", a, b)
	}
	if HashToken(a) == a || len(HashToken(a)) != 64 || HashToken(a) != HashToken(a) {
		t.Errorf("HashToken(%q) = %q", a, HashToken(a))
	}
}
