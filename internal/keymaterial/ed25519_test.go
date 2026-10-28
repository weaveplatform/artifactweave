package keymaterial_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/keymaterial"
)

func TestUnitEd25519KeyMaterial(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsa := rsaKey(t)
	priv, pub := pkcs8(t, private), pkixPEM(t, public)
	if err = keymaterial.CheckPair(keymaterial.Ed25519, priv, pub, ""); err != nil {
		t.Fatal(err)
	}
	parsed, err := keymaterial.Ed25519PrivateKey(priv, "")
	if err != nil || !parsed.Equal(private) {
		t.Fatalf("private key: %v", err)
	}
	parsedPub, err := keymaterial.Ed25519PublicKey(pub)
	if err != nil || !parsedPub.Equal(public) {
		t.Fatalf("public key: %v", err)
	}
	for name, tc := range map[string]struct{ private, public string }{
		"missing private": {"", pub}, "RSA private": {pkcs8(t, rsa), pub},
		"missing public": {priv, ""}, "wrong PEM block": {priv, pemOf("RSA PUBLIC KEY", []byte{1})},
		"invalid public DER": {priv, pemOf("PUBLIC KEY", []byte{1})},
		"RSA public":         {priv, pkixPEM(t, &rsa.PublicKey)}, "mismatched pair": {priv, pkixPEM(t, other)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := keymaterial.CheckPair(
				keymaterial.Ed25519,
				tc.private,
				tc.public,
				"",
			); !errors.Is(
				err,
				keymaterial.ErrInvalid,
			) {
				t.Fatalf("invalid keypair accepted: %v", err)
			}
		})
	}
}
