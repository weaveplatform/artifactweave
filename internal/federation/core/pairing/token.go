package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrMalformedToken reports a pairing token that is not one this server
// issues: not three base64url parts, not EdDSA, or missing a claim.
var ErrMalformedToken = errors.New("malformed pairing token")

// ErrTokenSignature reports a pairing token whose signature does not verify
// with the issuer's key: it was altered after it was issued.
var ErrTokenSignature = errors.New("pairing token signature does not verify")

// nonceBytes is the size of a pairing nonce: 256 bits, so a nonce cannot be
// guessed within the token's 300 seconds.
const nonceBytes = 32

// Token is what a pairing token carries (decision 0021): the issuing
// instance's base URL, instance id and public-key fingerprint, the use case
// and a single-use nonce, with its issue and expiry times. It travels as a
// compact JWS (RFC 7515) with alg EdDSA, signed with the issuer's instance
// key, so the redeemer can tell an altered token once it holds the key whose
// fingerprint the token names.
type Token struct {
	BaseURL     string        `json:"base_url"`
	InstanceID  string        `json:"iss"`
	Fingerprint string        `json:"key_sha256"`
	UseCase     model.UseCase `json:"use_case"`
	Nonce       string        `json:"nonce"`
	IssuedAt    int64         `json:"iat"`
	ExpiresAt   int64         `json:"exp"`
}

// tokenHeader is the JWS protected header every pairing token carries.
var tokenHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))

// Fingerprint is the SHA-256 of an Ed25519 public key in lowercase hex, the
// form a pairing token names the issuer's key in.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// NonceHash is the SHA-256 of a nonce's bytes, the only form the issuer
// stores it in.
func NonceHash(nonce []byte) []byte {
	sum := sha256.Sum256(nonce)
	return sum[:]
}

// Expires is the token's expiry.
func (t Token) Expires() time.Time { return time.Unix(t.ExpiresAt, 0) }

// NonceBytes decodes the nonce the token carries.
func (t Token) NonceBytes() ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(t.Nonce)
	if err != nil || len(b) != nonceBytes {
		return nil, fmt.Errorf("%w: nonce", ErrMalformedToken)
	}
	return b, nil
}

// Sign encodes t as a compact JWS signed with key.
func (t Token) Sign(key ed25519.PrivateKey) (string, error) {
	// ed25519.Sign panics on a key of another size; an identity without
	// its key is a store fault, not a request's.
	if len(key) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("pairing token: the instance identity holds no Ed25519 key: %w", kernel.ErrStore)
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("encode pairing token: %w", err)
	}
	input := tokenHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input))), nil
}

// Parsed is a decoded pairing token whose signature is not yet checked: the
// redeemer learns the issuer's key only from the issuer's answer.
type Parsed struct {
	Token
	signed    []byte
	signature []byte
}

// Parse decodes a pairing token without verifying it. It refuses anything
// that is not an EdDSA compact JWS carrying every claim.
func Parse(raw string) (Parsed, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Parsed{}, fmt.Errorf("%w: not a compact JWS", ErrMalformedToken)
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Parsed{}, fmt.Errorf("%w: header", ErrMalformedToken)
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "EdDSA" {
		return Parsed{}, fmt.Errorf("%w: header", ErrMalformedToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Parsed{}, fmt.Errorf("%w: payload", ErrMalformedToken)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Parsed{}, fmt.Errorf("%w: signature", ErrMalformedToken)
	}
	var t Token
	// Unknown claims are ignored, as a later issuer may add some.
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&t); err != nil {
		return Parsed{}, fmt.Errorf("%w: payload", ErrMalformedToken)
	}
	if t.BaseURL == "" || t.InstanceID == "" || len(t.Fingerprint) != hex.EncodedLen(sha256.Size) ||
		t.UseCase == "" || t.ExpiresAt == 0 {
		return Parsed{}, fmt.Errorf("%w: missing claim", ErrMalformedToken)
	}
	if _, err := t.NonceBytes(); err != nil {
		return Parsed{}, err
	}
	return Parsed{Token: t, signed: []byte(parts[0] + "." + parts[1]), signature: signature}, nil
}

// Verify checks that pub is the key the token names and that it signed the
// token.
func (p Parsed) Verify(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize || Fingerprint(pub) != p.Fingerprint {
		return ErrKeyMismatch
	}
	if !ed25519.Verify(pub, p.signed, p.signature) {
		return ErrTokenSignature
	}
	return nil
}
