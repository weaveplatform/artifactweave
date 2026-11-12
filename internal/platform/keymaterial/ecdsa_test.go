package keymaterial_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

func ecKey(t *testing.T, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sec1(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pemOf("EC PRIVATE KEY", der)
}

// pgpPair is an armored OpenPGP pair whose primary key uses algo and curve,
// as gpg --expert --full-generate-key makes one, with its secret key
// encrypted when passphrase is set.
func pgpPair(
	t *testing.T,
	algo packet.PublicKeyAlgorithm,
	curve packet.Curve,
	passphrase string,
) (private, public string) {
	t.Helper()
	e, err := openpgp.NewEntity("npm", "signing", "npm@example.com",
		&packet.Config{Algorithm: algo, Curve: curve})
	if err != nil {
		t.Fatal(err)
	}
	public = armored(
		t,
		openpgp.PublicKeyType,
		func(w *bytes.Buffer) error { return e.Serialize(w) },
	)
	if passphrase == "" {
		return armored(t, openpgp.PrivateKeyType, func(w *bytes.Buffer) error {
			return e.SerializePrivate(w, nil)
		}), public
	}
	if err := e.EncryptPrivateKeys([]byte(passphrase), nil); err != nil {
		t.Fatal(err)
	}
	return armored(t, openpgp.PrivateKeyType, func(w *bytes.Buffer) error {
		return e.SerializePrivateWithoutSigning(w, nil)
	}), public
}

func TestUnitECDSAKeyMaterial(t *testing.T) {
	k, other := ecKey(t, elliptic.P256()), ecKey(t, elliptic.P256())
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:staticcheck // SA1019: the legacy format the checker must open
	enc, err := x509.EncryptPEMBlock(rand.Reader, "EC PRIVATE KEY", der, []byte("s3cret"),
		x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	p384 := ecKey(t, elliptic.P384())
	pgpPriv, pgpPub := pgpPair(t, packet.PubKeyAlgoECDSA, packet.CurveNistP256, "")
	pgpEnc, pgpEncPub := pgpPair(t, packet.PubKeyAlgoECDSA, packet.CurveNistP384, "s3cret")
	for name, c := range map[string]struct{ priv, pub, pass string }{
		"pkcs8 and pkix":     {pkcs8(t, k), pkixPEM(t, &k.PublicKey), ""},
		"sec1 and pkix":      {sec1(t, k), pkixPEM(t, &k.PublicKey), ""},
		"legacy encrypted":   {string(pem.EncodeToMemory(enc)), pkixPEM(t, &k.PublicKey), "s3cret"},
		"certificate public": {sec1(t, k), pemOf("CERTIFICATE", selfSigned(t, k, "npm")), ""},
		"P-384":              {pkcs8(t, p384), pkixPEM(t, &p384.PublicKey), ""},
		"P-521":              {sec1(t, ecKey(t, elliptic.P521())), "", ""},
		"gpg export":         {pgpPriv, pgpPub, ""},
		"gpg passphrase":     {pgpEnc, pgpEncPub, "s3cret"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.pub == "" {
				parsed, err := keymaterial.ECDSAPrivateKey(c.priv, c.pass)
				if err != nil {
					t.Fatal(err)
				}
				c.pub = pkixPEM(t, &parsed.PublicKey)
			}
			if err := keymaterial.CheckPair(keymaterial.ECDSA, c.priv, c.pub, c.pass); err != nil {
				t.Fatal(err)
			}
		})
	}
	// The PEM and OpenPGP forms of one key read back as the same key.
	parsed, err := keymaterial.ECDSAPrivateKey(pgpPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	parsedPub, err := keymaterial.ECDSAPublicKey(pgpPub)
	if err != nil || !parsed.PublicKey.Equal(parsedPub) {
		t.Fatalf("OpenPGP halves: %v", err)
	}
	if err := keymaterial.CheckPair(
		keymaterial.ECDSA,
		pgpPriv,
		pkixPEM(t, parsedPub),
		"",
	); err != nil {
		t.Fatalf("OpenPGP private with PEM public: %v", err)
	}

	edPriv, edPub := pgpPair(t, packet.PubKeyAlgoEdDSA, packet.Curve25519, "")
	k1Priv, k1Pub := pgpPair(t, packet.PubKeyAlgoECDSA, packet.CurveBrainpoolP256, "")
	p224 := ecKey(t, elliptic.P224())
	rsa := rsaKey(t)
	pub := pkixPEM(t, &k.PublicKey)
	for name, c := range map[string]struct{ priv, pub, pass, expected string }{
		"mismatch":               {pkcs8(t, k), pkixPEM(t, &other.PublicKey), "", "do not match"},
		"wrong passphrase":       {string(pem.EncodeToMemory(enc)), pub, "nope", "passphrase"},
		"RSA private":            {pkcs8(t, rsa), pub, "", "private key is not an ECDSA key"},
		"RSA public":             {pkcs8(t, k), pkixPEM(t, &rsa.PublicKey), "", "public key is not an ECDSA key"},
		"P-224 private":          {pkcs8(t, p224), pub, "", "curve must be P-256, P-384 or P-521, not P-224"},
		"P-224 public":           {pkcs8(t, k), pkixPEM(t, &p224.PublicKey), "", "not P-224"},
		"no private block":       {"nothing", pub, "", "no private key"},
		"public not PEM":         {pkcs8(t, k), "nothing", "", "not PEM"},
		"public block type":      {pkcs8(t, k), pemOf("SOMETHING", []byte{1}), "", "unsupported"},
		"bad public DER":         {pkcs8(t, k), pemOf("PUBLIC KEY", []byte{1}), "", "does not parse"},
		"bad certificate":        {pkcs8(t, k), pemOf("CERTIFICATE", []byte{1}), "", "does not parse"},
		"gpg EdDSA private":      {edPriv, pub, "", "private key is not an ECDSA key"},
		"gpg EdDSA public":       {pkcs8(t, k), edPub, "", "public key is not an ECDSA key"},
		"gpg brainpool private":  {k1Priv, pub, "", "not brainpoolP256r1"},
		"gpg brainpool public":   {pkcs8(t, k), k1Pub, "", "not brainpoolP256r1"},
		"gpg wrong passphrase":   {pgpEnc, pgpEncPub, "nope", "passphrase"},
		"gpg public as private":  {pgpPub, pgpPub, "", "no private key"},
		"gpg mismatch":           {pgpPriv, pgpEncPub, "", "do not match"},
		"gpg bad private armor":  {"-----BEGIN PGP PRIVATE KEY BLOCK-----\n", pub, "", "private key is not"},
		"gpg bad public armor":   {pkcs8(t, k), "-----BEGIN PGP PUBLIC KEY BLOCK-----\n", "", "public key is not"},
		"gpg secret, bad public": {pgpPriv, "-----BEGIN PGP PUBLIC KEY BLOCK-----\n", "", "public key is not"},
	} {
		t.Run(name, func(t *testing.T) {
			err := keymaterial.CheckPair(keymaterial.ECDSA, c.priv, c.pub, c.pass)
			if !errors.Is(err, keymaterial.ErrInvalid) ||
				!strings.Contains(err.Error(), c.expected) {
				t.Fatalf("%v, want an invalid error mentioning %q", err, c.expected)
			}
		})
	}
}
