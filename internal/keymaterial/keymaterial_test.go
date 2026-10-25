package keymaterial_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/weaveplatform/artifactweave/internal/keymaterial"
)

// Keys are generated here, never committed: a private key in the
// repository would also trip secret scanning.
func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pemOf(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

func pkcs8(t *testing.T, k crypto.Signer) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pemOf("PRIVATE KEY", der)
}

func pkixPEM(t *testing.T, pub crypto.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pemOf("PUBLIC KEY", der)
}

func selfSigned(t *testing.T, k crypto.Signer, cn string) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, k.Public(), k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestUnitCheckRSA(t *testing.T) {
	k, other := rsaKey(t), rsaKey(t)
	pkcs1 := pemOf("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(k))
	//nolint:staticcheck // SA1019: the legacy format the checker must open
	enc, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY",
		x509.MarshalPKCS1PrivateKey(k), []byte("s3cret"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := string(pem.EncodeToMemory(enc))
	cert := pemOf("CERTIFICATE", selfSigned(t, k, "rsa"))
	for name, c := range map[string]struct{ priv, pub, pass string }{
		"pkcs1 and pkix":         {pkcs1, pkixPEM(t, &k.PublicKey), ""},
		"pkcs8 and pkcs1 public": {pkcs8(t, k), pemOf("RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&k.PublicKey)), ""},
		"certificate public":     {pkcs1, cert, ""},
		"passphrase":             {encrypted, pkixPEM(t, &k.PublicKey), "s3cret"},
		"other blocks first":     {cert + pkcs1, pkixPEM(t, &k.PublicKey), ""},
	} {
		if err := keymaterial.CheckPair(keymaterial.RSA, c.priv, c.pub, c.pass); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ typ, priv, pub, pass, want string }{
		"mismatch":          {keymaterial.RSA, pkcs1, pkixPEM(t, &other.PublicKey), "", "do not match"},
		"wrong passphrase":  {keymaterial.RSA, encrypted, pkixPEM(t, &k.PublicKey), "nope", "passphrase"},
		"ec private":        {keymaterial.RSA, pkcs8(t, ec), pkixPEM(t, &k.PublicKey), "", "not an RSA key"},
		"ec public":         {keymaterial.RSA, pkcs1, pkixPEM(t, &ec.PublicKey), "", "not an RSA key"},
		"pkcs8 encrypted":   {keymaterial.RSA, pemOf("ENCRYPTED PRIVATE KEY", []byte{1}), pkixPEM(t, &k.PublicKey), "", "not supported"},
		"no private block":  {keymaterial.RSA, "nothing", pkixPEM(t, &k.PublicKey), "", "no private key"},
		"bad private DER":   {keymaterial.RSA, pemOf("RSA PRIVATE KEY", []byte{1}), pkixPEM(t, &k.PublicKey), "", "does not parse"},
		"public not PEM":    {keymaterial.RSA, pkcs1, "nothing", "", "not PEM"},
		"public block type": {keymaterial.RSA, pkcs1, pemOf("SOMETHING", []byte{1}), "", "unsupported"},
		"bad public DER":    {keymaterial.RSA, pkcs1, pemOf("PUBLIC KEY", []byte{1}), "", "does not parse"},
		"unknown type":      {"DSA", pkcs1, pkixPEM(t, &k.PublicKey), "", "RSA or GPG"},
	} {
		err := keymaterial.CheckPair(c.typ, c.priv, c.pub, c.pass)
		if !errors.Is(err, keymaterial.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an invalid error mentioning %q", name, err, c.want)
		}
	}
}

// Legacy PEM encryption checks only the CBC padding, so about 1 wrong
// passphrase in 256 decrypts without error into DER that does not parse.
// TestUnitCheckRSA's random IV hit that case once in a few hundred runs; here a
// fixed IV and a search over passphrases hit it on every run.
func TestUnitCheckRSAWrongPassphraseWithValidPadding(t *testing.T) {
	k := rsaKey(t)
	iv := bytes.NewReader(bytes.Repeat([]byte{7}, 16))
	//nolint:staticcheck // SA1019: the legacy format the checker must open
	block, err := x509.EncryptPEMBlock(iv, "RSA PRIVATE KEY",
		x509.MarshalPKCS1PrivateKey(k), []byte("s3cret"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	pass := ""
	for i := range 1 << 16 {
		candidate := fmt.Sprintf("wrong-%d", i)
		//nolint:staticcheck // SA1019: as above
		if _, err := x509.DecryptPEMBlock(block, []byte(candidate)); err == nil {
			pass = candidate
			break
		}
	}
	if pass == "" {
		t.Fatal("no wrong passphrase in 65536 passes the padding check")
	}
	err = keymaterial.CheckPair(keymaterial.RSA, string(pem.EncodeToMemory(block)),
		pkixPEM(t, &k.PublicKey), pass)
	if !errors.Is(err, keymaterial.ErrInvalid) || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("%q: %v, want an invalid error mentioning \"passphrase\"", pass, err)
	}
}

func armored(t *testing.T, typ string, write func(w *bytes.Buffer) error) string {
	t.Helper()
	var raw bytes.Buffer
	if err := write(&raw); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := armor.Encode(&out, typ, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func gpgPair(t *testing.T, passphrase string) (private, public string) {
	t.Helper()
	e, err := openpgp.NewEntity("artifactweave", "test", "test@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	public = armored(
		t,
		openpgp.PublicKeyType,
		func(w *bytes.Buffer) error { return e.Serialize(w) },
	)
	if passphrase != "" {
		if err := e.EncryptPrivateKeys([]byte(passphrase), nil); err != nil {
			t.Fatal(err)
		}
		private = armored(t, openpgp.PrivateKeyType, func(w *bytes.Buffer) error {
			return e.SerializePrivateWithoutSigning(w, nil)
		})
		return private, public
	}
	private = armored(t, openpgp.PrivateKeyType, func(w *bytes.Buffer) error {
		return e.SerializePrivate(w, nil)
	})
	return private, public
}

func TestUnitCheckGPG(t *testing.T) {
	priv, pub := gpgPair(t, "")
	encPriv, encPub := gpgPair(t, "s3cret")
	_, otherPub := gpgPair(t, "")
	if err := keymaterial.CheckPair(keymaterial.GPG, priv, pub, ""); err != nil {
		t.Errorf("matching pair: %v", err)
	}
	if err := keymaterial.CheckPair(keymaterial.GPG, encPriv, encPub, "s3cret"); err != nil {
		t.Errorf("encrypted pair: %v", err)
	}
	for name, c := range map[string]struct{ priv, pub, pass, want string }{
		"mismatch":         {priv, otherPub, "", "do not match"},
		"wrong passphrase": {encPriv, encPub, "nope", "passphrase"},
		"public as secret": {pub, pub, "", "no private key"},
		"bad private":      {"not armor", pub, "", "private key is not"},
		"bad public":       {priv, "not armor", "", "public key is not"},
		"empty armor":      {armored(t, openpgp.PrivateKeyType, func(*bytes.Buffer) error { return nil }), pub, "", "private key"},
	} {
		err := keymaterial.CheckPair(keymaterial.GPG, c.priv, c.pub, c.pass)
		if !errors.Is(err, keymaterial.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an invalid error mentioning %q", name, err, c.want)
		}
	}
}

func TestUnitParseBundle(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := pemOf("CERTIFICATE", selfSigned(t, ec, "client.example.com"))
	chain := pemOf("CERTIFICATE", selfSigned(t, ec, "intermediate"))
	ecDER, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	key := pemOf("EC PRIVATE KEY", ecDER)
	b, err := keymaterial.ParseBundle([]byte(leaf + chain + key))
	if err != nil {
		t.Fatal(err)
	}
	if b.IssuedTo != "client.example.com" || b.IssuedBy != "client.example.com" ||
		!b.IssuedOn.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!b.ValidUntil.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		b.Certificates != leaf+chain || b.PrivateKey != key ||
		!regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(b.Fingerprint) {
		t.Fatalf("bundle %+v", b)
	}
	// A distinguished name stands in for a missing common name; PKCS#1 and
	// PKCS#8 keys are accepted.
	rk := rsaKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{Organization: []string{"Weave"}},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &rk.PublicKey, rk)
	if err != nil {
		t.Fatal(err)
	}
	rsaCert := pemOf("CERTIFICATE", der)
	for _, k := range []string{pemOf("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rk)), pkcs8(t, rk)} {
		b, err := keymaterial.ParseBundle([]byte(k + rsaCert))
		if err != nil || b.IssuedTo != "O=Weave" {
			t.Fatalf("rsa bundle %+v, %v", b, err)
		}
	}
	for name, c := range map[string]struct{ data, want string }{
		"no certificate":    {key, "no certificate"},
		"no key":            {leaf, "no private key"},
		"other key":         {leaf + pkcs8(t, rk), "does not belong"},
		"encrypted pkcs8":   {leaf + pemOf("ENCRYPTED PRIVATE KEY", []byte{1}), "must not be encrypted"},
		"bad certificate":   {pemOf("CERTIFICATE", []byte{1}) + key, "certificate does not parse"},
		"bad key":           {leaf + pemOf("EC PRIVATE KEY", []byte{1}), "private key does not parse"},
		"legacy encryption": {leaf + "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-256-CBC,00\n\nAAAA\n-----END RSA PRIVATE KEY-----\n", "must not be encrypted"},
	} {
		_, err := keymaterial.ParseBundle([]byte(c.data))
		if !errors.Is(err, keymaterial.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an invalid error mentioning %q", name, err, c.want)
		}
	}
}
