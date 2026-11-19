package keymaterial_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

func TestUnitPublicFieldRejectsPrivateMaterial(t *testing.T) {
	rsa := rsaKey(t)
	ec := ecKey(t, elliptic.P256())
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gpgPriv, gpgPub := gpgPair(t, "")
	ecPGPPriv, ecPGPPub := pgpPair(t, packet.PubKeyAlgoECDSA, packet.CurveNistP256, "")
	for _, tc := range []struct{ name, kind, private, public string }{
		{"RSA trailing private", keymaterial.RSA, pkcs8(t, rsa), pkixPEM(t, &rsa.PublicKey) + pkcs8(t, rsa)},
		{"ECDSA trailing private", keymaterial.ECDSA, pkcs8(t, ec), pkixPEM(t, &ec.PublicKey) + pkcs8(t, ec)},
		{"Ed25519 trailing private", keymaterial.Ed25519, pkcs8(t, edPriv), pkixPEM(t, edPub) + pkcs8(t, edPriv)},
		{"GPG private block", keymaterial.GPG, gpgPriv, gpgPriv},
		{"GPG mislabeled private", keymaterial.GPG, gpgPriv, strings.ReplaceAll(gpgPriv, "PGP PRIVATE KEY BLOCK", "PGP PUBLIC KEY BLOCK")},
		{"GPG trailing private", keymaterial.GPG, gpgPriv, gpgPub + gpgPriv},
		{"ECDSA armored private", keymaterial.ECDSA, ecPGPPriv, ecPGPPriv},
		{"ECDSA armored trailing private", keymaterial.ECDSA, ecPGPPriv, ecPGPPub + ecPGPPriv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := keymaterial.CheckPair(tc.kind, tc.private, tc.public, ""); !errors.Is(err, keymaterial.ErrInvalid) {
				t.Fatal("private-bearing public field was accepted")
			}
		})
	}
}

func TestUnitCheckPublicConsumesEveryBlockAndPacket(t *testing.T) {
	priv, pub := gpgPair(t, "")
	rsa := rsaKey(t)
	pemPublic := pkixPEM(t, &rsa.PublicKey)
	for _, tc := range []struct {
		name, kind, value string
		valid             bool
	}{
		{"GPG public", keymaterial.GPG, pub, true},
		{"GPG multiple public blocks", keymaterial.GPG, pub + "\n" + pub, true},
		{"GPG CRLF", keymaterial.GPG, strings.ReplaceAll(pub, "\n", "\r\n"), true},
		{"RSA public chain", keymaterial.RSA, pemPublic + pemOf("CERTIFICATE", selfSigned(t, rsa, "public")), true},
		{"RSA whitespace", keymaterial.RSA, " \n" + pemPublic + "\n\t", true},
		{"empty", keymaterial.GPG, "", false},
		{"GPG trailing garbage", keymaterial.GPG, pub + "not a key", false},
		{"GPG invalid header", keymaterial.GPG, strings.Replace(pub, "\n\n", "\ninvalid header\n\n", 1), false},
		{"GPG missing footer", keymaterial.GPG, strings.Replace(pub, "-----END PGP PUBLIC KEY BLOCK-----", "", 1), false},
		{"GPG invalid base64", keymaterial.GPG, "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n!invalid\n-----END PGP PUBLIC KEY BLOCK-----", false},
		{"GPG invalid CRC", keymaterial.GPG, "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nYQ==\n=bad\n-----END PGP PUBLIC KEY BLOCK-----", false},
		{"GPG invalid packet", keymaterial.GPG, "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nYQ==\n-----END PGP PUBLIC KEY BLOCK-----", false},
		{"empty PEM", keymaterial.RSA, "", false},
		{"PEM bad public DER", keymaterial.RSA, pemOf("PUBLIC KEY", []byte{1}), false},
		{"PEM nonpublic block", keymaterial.RSA, pemOf("PRIVATE KEY", []byte{1}), false},

		{"GPG secret before public", keymaterial.GPG, priv + pub, false},
		{"GPG secret inside public armor", keymaterial.GPG, strings.ReplaceAll(priv, "PGP PRIVATE KEY BLOCK", "PGP PUBLIC KEY BLOCK"), false},
		{"GPG content after checksum", keymaterial.GPG, strings.Replace(pub, "-----END PGP PUBLIC KEY BLOCK-----", priv+"-----END PGP PUBLIC KEY BLOCK-----", 1), false},
		{"PEM trailing garbage", keymaterial.RSA, pemPublic + "not a key", false},
		{"PEM malformed prefix", keymaterial.RSA, "-----BEGIN PUBLIC KEY-----\nbad\n-----END PUBLIC KEY-----\n" + pemPublic, false},
		{"PEM nested private armor", keymaterial.RSA, "-----BEGIN PUBLIC KEY-----\n" + priv + "\n" + pemPublic, false},
		{"PEM public header", keymaterial.RSA, strings.Replace(pemPublic, "-----BEGIN PUBLIC KEY-----\n", "-----BEGIN PUBLIC KEY-----\nComment: public fixture\n\n", 1), true},
		{"PEM private before public", keymaterial.RSA, pkcs8(t, rsa) + pemPublic, false},
		{"wrong algorithm", keymaterial.Ed25519, pemPublic, false},
		{"unknown pair type", "unknown", pemPublic, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := keymaterial.CheckPublic(tc.kind, tc.value)
			if (err == nil) != tc.valid {
				t.Fatalf("public validation: valid=%t err=%v", tc.valid, err)
			}
			if err != nil && (!errors.Is(err, keymaterial.ErrInvalid) || strings.Contains(err.Error(), "-----BEGIN")) {
				t.Fatal("public validation error lost its type or included material")
			}
		})
	}
}

func TestUnitCheckPublicRejectsSecretSubkeysAndLaterEntities(t *testing.T) {
	e, err := openpgp.NewEntity("public-fixture", "", "fixture@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	var public bytes.Buffer
	if err := e.Serialize(&public); err != nil {
		t.Fatal(err)
	}
	var subkey bytes.Buffer
	subkey.Write(public.Bytes())
	if err := e.Subkeys[0].PrivateKey.Serialize(&subkey); err != nil {
		t.Fatal(err)
	}
	if err := e.Subkeys[0].Sig.Serialize(&subkey); err != nil {
		t.Fatal(err)
	}
	var later bytes.Buffer
	later.Write(public.Bytes())
	if err := e.SerializePrivate(&later, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{{"private subkey", subkey.Bytes()}, {"later private entity", later.Bytes()}} {
		t.Run(tc.name, func(t *testing.T) {
			value := armored(t, openpgp.PublicKeyType, func(w *bytes.Buffer) error { _, err := w.Write(tc.raw); return err })
			if err := keymaterial.CheckPublic(keymaterial.GPG, value); !errors.Is(err, keymaterial.ErrInvalid) {
				t.Fatal("private packets in public armor were accepted")
			}
		})
	}
}
