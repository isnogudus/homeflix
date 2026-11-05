// Package passkeytest has a software authenticator for tests of passkey
// registration and sign-in.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
)

var b64 = base64.RawURLEncoding

// Authenticator is a software passkey: one P-256 key, as a platform
// authenticator would make it. Count is its signature counter.
type Authenticator struct {
	t          testing.TB
	key        *ecdsa.PrivateKey
	ID         []byte
	rpID       string
	userHandle []byte
	Count      uint32
}

// New makes an authenticator with a fresh key.
func New(t testing.TB) *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &Authenticator{t: t, key: key, ID: id}
}

func (a *Authenticator) clientData(kind, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

func (a *Authenticator) authData(flags byte, attested []byte) []byte {
	h := sha256.Sum256([]byte(a.rpID))
	out := append(h[:], flags)
	out = binary.BigEndian.AppendUint32(out, a.Count)
	return append(out, attested...)
}

// Create answers navigator.credentials.create for the options the server
// sent, as a browser on origin would.
func (a *Authenticator) Create(publicKey json.RawMessage, origin string) []byte {
	var pk struct {
		Challenge string              `json:"challenge"`
		RP        struct{ ID string } `json:"rp"`
		User      struct{ ID string } `json:"user"`
	}
	if err := json.Unmarshal(publicKey, &pk); err != nil {
		a.t.Fatal(err)
	}
	a.rpID = pk.RP.ID
	handle, err := b64.DecodeString(pk.User.ID)
	if err != nil {
		a.t.Fatal(err)
	}
	a.userHandle = handle
	pub := a.key.PublicKey
	x, y := make([]byte, 32), make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		a.t.Fatal(err)
	}
	attested := make([]byte, 16) // AAGUID
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(a.ID)))
	attested = append(append(attested, a.ID...), cose...)
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{},
		"authData": a.authData(0x45, attested)}) // UP, UV, AT
	if err != nil {
		a.t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.ID), "rawId": b64.EncodeToString(a.ID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", pk.Challenge, origin)),
			"attestationObject": b64.EncodeToString(att),
		},
		"clientExtensionResults": map[string]any{},
	})
	return resp
}

// Get answers navigator.credentials.get.
func (a *Authenticator) Get(publicKey json.RawMessage, origin string) []byte {
	var pk struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(publicKey, &pk); err != nil {
		a.t.Fatal(err)
	}
	a.Count++
	ad := a.authData(0x05, nil) // UP, UV
	cd := a.clientData("webauthn.get", pk.Challenge, origin)
	h := sha256.Sum256(cd)
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, sha256Sum(append(append([]byte{}, ad...), h[:]...)))
	if err != nil {
		a.t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.ID), "rawId": b64.EncodeToString(a.ID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.userHandle),
		},
		"clientExtensionResults": map[string]any{},
	})
	return resp
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
