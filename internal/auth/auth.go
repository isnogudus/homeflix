// Package auth hashes passwords and creates access tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Params are Argon2id cost parameters.
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// HashParams apply to new hashes. Verification reads the parameters from
// the stored hash, so they can change without breaking existing passwords.
// Tests lower them to stay fast.
var HashParams = Params{MemoryKiB: 64 * 1024, Time: 3, Threads: 2}

const (
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns an Argon2id hash in PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := HashParams
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("unsupported password hash")

// VerifyPassword reports whether password matches hash.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// VerifyNoUser spends the same time as VerifyPassword so that a login for
// an unknown name cannot be told apart from a wrong password by timing.
func VerifyNoUser(password string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("homeflix-dummy") })
	VerifyPassword(dummyHash, password)
}

// NewToken returns a random access token as 32 lowercase hex digits, the
// format Jellyfin clients expect.
func NewToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// HashToken returns the value under which a token is stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
