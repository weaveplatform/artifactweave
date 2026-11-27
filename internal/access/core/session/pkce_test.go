package session_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/session"
)

// RFC 7636 Appendix B supplies both strings independently of this implementation.
const (
	rfcVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func TestUnitPKCEAcceptsRFC7636VectorAndVerifierBounds(t *testing.T) {
	if err := session.ValidatePKCEChallenge(rfcChallenge, "S256"); err != nil {
		t.Fatal(err)
	}
	if !session.VerifyPKCE(rfcChallenge, rfcVerifier) {
		t.Fatal("RFC 7636 verifier did not redeem its published challenge")
	}
	for _, verifier := range []string{strings.Repeat("a", 43), strings.Repeat("-._~", 32)} {
		hash := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(hash[:])
		if !session.VerifyPKCE(challenge, verifier) {
			t.Fatalf("valid RFC verifier of length %d was rejected", len(verifier))
		}
	}
}

func TestUnitPKCERejectsDowngradeMalformedInputAndOtherVerifier(t *testing.T) {
	for _, tc := range []struct{ challenge, method string }{
		{rfcVerifier, "plain"},
		{rfcChallenge, ""},
		{rfcChallenge, "s256"},
		{rfcChallenge + "=", "S256"},
		{rfcChallenge[:42], "S256"},
		{strings.Repeat("!", 43), "S256"},
		{"\n" + strings.Repeat("A", 42), "S256"},
		// Nonzero trailing bits can decode to the same bytes in a lenient
		// decoder; reject the alternate spelling before storing a challenge.
		{rfcChallenge[:42] + "N", "S256"},
	} {
		if err := session.ValidatePKCEChallenge(tc.challenge, tc.method); !errors.Is(err, session.ErrInvalidRequest) {
			t.Fatalf("invalid PKCE start accepted: %v", err)
		}
	}
	for _, verifier := range []string{
		"", strings.Repeat("a", 42), strings.Repeat("a", 129),
		strings.Repeat("é", 43), rfcVerifier[:42] + "+", rfcVerifier[:42] + " ",
		strings.Repeat("z", 43),
	} {
		if session.VerifyPKCE(rfcChallenge, verifier) {
			t.Fatal("malformed or unrelated verifier redeemed the code")
		}
	}
	if session.VerifyPKCE("malformed", rfcVerifier) {
		t.Fatal("malformed stored challenge accepted a verifier")
	}
}

func TestUnitNativeCallbackKeepsPinnedClientPathAndInclusivePortEdges(t *testing.T) {
	ports := session.PortRange{Min: 10000, Max: 10010}
	for _, raw := range []string{"http://localhost:10000/login", "http://localhost:10010/login"} {
		if err := session.ValidateRedirect(raw, ports); err != nil {
			t.Fatalf("pinned client's callback rejected: %v", err)
		}
	}
	for _, raw := range []string{
		"", "//localhost:10000/login", "https://localhost:10000/login",
		"http://127.0.0.1:10000/login", "http://[::1]:10000/login",
		"http://localhost.evil.invalid:10000/login", "http://localhost.:10000/login",
		"http://LOCALHOST:10000/login", "http://user@localhost:10000/login",
		"http://localhost:9999/login", "http://localhost:10011/login",
		"http://localhost:010000/login", "http://localhost/login",
		"http://localhost:invalid/login", "http://localhost:10000/",
		"http://localhost:10000/%6cogin", "http://localhost:10000/a/../login",
		"http://localhost:10000/login?code=other", "http://localhost:10000/login?",
		"http://localhost:10000/login#fragment", "http://localhost:10000/login#",
		"http://localhost:10000/login\r\n",
	} {
		if err := session.ValidateRedirect(raw, ports); !errors.Is(err, session.ErrInvalidRequest) {
			t.Fatalf("untrusted callback accepted: %v", err)
		}
	}
	for _, invalid := range []session.PortRange{
		{Min: 1023, Max: 10010},
		{Min: 10000, Max: 65536},
		{Min: 10000, Max: 10000},
		{Min: 10010, Max: 10000},
	} {
		if err := session.ValidateRedirect("http://localhost:10000/login", invalid); !errors.Is(err, session.ErrInvalidRequest) {
			t.Fatalf("unusable advertised interval accepted: %+v %v", invalid, err)
		}
	}
}
