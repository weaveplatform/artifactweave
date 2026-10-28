// Package keymaterial checks the key material the security API stores: that
// the private and public halves of an RSA or GPG key pair belong together,
// and that a client certificate bundle carries its own private key.
package keymaterial

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// ErrInvalid reports key material that does not parse, does not match, or
// cannot be unlocked. Its message is safe to show the caller.
var ErrInvalid = errors.New("keymaterial: invalid key material")

// Key pair types the security API accepts.
const (
	RSA = "RSA"
	GPG = "GPG"
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// CheckPair verifies that privateKey and publicKey are the two halves of one
// key of pairType, unlocking privateKey with passphrase when it is
// encrypted.
func CheckPair(pairType, privateKey, publicKey, passphrase string) error {
	switch pairType {
	case RSA:
		return checkRSA(privateKey, publicKey, passphrase)
	case GPG:
		return checkGPG(privateKey, publicKey, passphrase)
	case Ed25519:
		return checkEd25519(privateKey, publicKey, passphrase)
	default:
		return invalidf("pairType must be RSA, GPG or Ed25519, not %q", pairType)
	}
}

func checkRSA(privateKey, publicKey, passphrase string) error {
	priv, err := parsePrivateKey([]byte(privateKey), passphrase)
	if err != nil {
		return err
	}
	rk, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return invalidf("the private key is not an RSA key")
	}
	pub, err := parseRSAPublicKey(publicKey)
	if err != nil {
		return err
	}
	if !rk.PublicKey.Equal(pub) {
		return invalidf("the private and public keys do not match")
	}
	return nil
}

// RSAPrivateKey opens a validated RSA key pair for format metadata signing.
// It accepts the same PEM and legacy PEM passphrase forms as CheckPair.
func RSAPrivateKey(privateKey, passphrase string) (*rsa.PrivateKey, error) {
	signer, err := parsePrivateKey([]byte(privateKey), passphrase)
	if err != nil {
		return nil, err
	}
	key, ok := signer.(*rsa.PrivateKey)
	if !ok {
		return nil, invalidf("the private key is not an RSA key")
	}
	return key, nil
}

func parseRSAPublicKey(s string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, invalidf("the public key is not PEM")
	}
	var key any
	var err error
	switch block.Type {
	case "PUBLIC KEY":
		key, err = x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	case "CERTIFICATE":
		var c *x509.Certificate
		if c, err = x509.ParseCertificate(block.Bytes); err == nil {
			key = c.PublicKey
		}
	default:
		return nil, invalidf("unsupported public key block %q", block.Type)
	}
	if err != nil {
		return nil, invalidf("the public key does not parse: %v", err)
	}
	pub, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, invalidf("the public key is not an RSA key")
	}
	return pub, nil
}

// parsePrivateKey reads the first private key block of data: PKCS#1, PKCS#8
// or SEC 1, optionally encrypted the legacy OpenSSL way (Proc-Type
// encrypted PEM, what `openssl genrsa -aes256` writes).
func parsePrivateKey(data []byte, passphrase string) (crypto.Signer, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, invalidf("no private key block")
		}
		if !strings.HasSuffix(block.Type, "PRIVATE KEY") {
			continue
		}
		if block.Type == "ENCRYPTED PRIVATE KEY" {
			return nil, invalidf("encrypted PKCS#8 keys are not supported; " +
				"supply an unencrypted key or a passphrase-protected PKCS#1 key")
		}
		der := block.Bytes
		//nolint:staticcheck // SA1019: legacy encrypted PEM is still what
		// OpenSSL writes for a passphrase-protected PKCS#1 key, and the
		// standard library has no other way to open it.
		encrypted := x509.IsEncryptedPEMBlock(block)
		if encrypted {
			var err error
			//nolint:staticcheck // SA1019: as above
			if der, err = x509.DecryptPEMBlock(block, []byte(passphrase)); err != nil {
				return nil, invalidf("the passphrase does not unlock the private key")
			}
		}
		key, err := parsePrivateDER(block.Type, der)
		if err != nil {
			// Legacy PEM encryption has no integrity check: about 1 wrong
			// passphrase in 256 passes the padding check and decrypts to
			// garbage. An unsupported key type (ErrInvalid) still says so.
			if encrypted && !errors.Is(err, ErrInvalid) {
				return nil, invalidf("the passphrase does not unlock the private key")
			}
			return nil, invalidf("the private key does not parse: %v", err)
		}
		return key, nil
	}
}

func parsePrivateDER(blockType string, der []byte) (crypto.Signer, error) {
	switch blockType {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(der) //nolint:wrapcheck // wrapped by the caller
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(der) //nolint:wrapcheck // wrapped by the caller
	default:
		key, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return nil, err //nolint:wrapcheck // wrapped by the caller
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, invalidf("unsupported key type %T", key)
		}
		return signer, nil
	}
}

func checkGPG(privateKey, publicKey, passphrase string) error {
	secret, err := readEntity(privateKey, "private")
	if err != nil {
		return err
	}
	if secret.PrivateKey == nil {
		return invalidf("the private key block holds no private key")
	}
	if secret.PrivateKey.Encrypted {
		if err := secret.DecryptPrivateKeys([]byte(passphrase)); err != nil {
			return invalidf("the passphrase does not unlock the private key")
		}
	}
	public, err := readEntity(publicKey, "public")
	if err != nil {
		return err
	}
	if !bytes.Equal(secret.PrimaryKey.Fingerprint, public.PrimaryKey.Fingerprint) {
		return invalidf("the private and public keys do not match")
	}
	return nil
}

func readEntity(armored, which string) (*openpgp.Entity, error) {
	list, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil {
		return nil, invalidf("the %s key is not an ASCII-armored OpenPGP key: %v", which, err)
	}
	if len(list) == 0 {
		return nil, invalidf("the %s key block holds no key", which)
	}
	return list[0], nil
}

// Bundle is a parsed client certificate bundle.
type Bundle struct {
	// Certificates is the PEM of every certificate in the bundle, the leaf
	// first; PrivateKey the PEM of its key.
	Certificates string
	PrivateKey   string
	IssuedTo     string
	IssuedBy     string
	IssuedOn     time.Time
	ValidUntil   time.Time
	// Fingerprint is the leaf's SHA-256 in colon-separated upper-case hex,
	// as Java's keytool prints it.
	Fingerprint string
}

// ParseBundle reads a PEM bundle of a certificate (optionally followed by
// its chain) and the certificate's private key, and checks the key belongs
// to the first certificate.
func ParseBundle(data []byte) (Bundle, error) {
	var (
		certs   [][]byte
		keyPEM  []byte
		rest    = data
		leaf    *x509.Certificate
		keyType string
		keyDER  []byte
	)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE":
			certs = append(certs, pem.EncodeToMemory(block))
			if leaf == nil {
				c, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					return Bundle{}, invalidf("the certificate does not parse: %v", err)
				}
				leaf = c
			}
		case strings.HasSuffix(block.Type, "PRIVATE KEY") && keyPEM == nil:
			if block.Type == "ENCRYPTED PRIVATE KEY" || block.Headers["Proc-Type"] != "" {
				return Bundle{}, invalidf("the private key must not be encrypted")
			}
			keyPEM, keyType, keyDER = pem.EncodeToMemory(block), block.Type, block.Bytes
		}
	}
	switch {
	case leaf == nil:
		return Bundle{}, invalidf("the bundle holds no certificate")
	case keyPEM == nil:
		return Bundle{}, invalidf("the bundle holds no private key")
	}
	key, err := parsePrivateDER(keyType, keyDER)
	if err != nil {
		return Bundle{}, invalidf("the private key does not parse: %v", err)
	}
	pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(leaf.PublicKey) {
		return Bundle{}, invalidf("the private key does not belong to the certificate")
	}
	sum := sha256.Sum256(leaf.Raw)
	hexed := strings.ToUpper(hex.EncodeToString(sum[:]))
	pairs := make([]string, 0, len(sum))
	for i := 0; i < len(hexed); i += 2 {
		pairs = append(pairs, hexed[i:i+2])
	}
	return Bundle{
		Certificates: string(bytes.Join(certs, nil)),
		PrivateKey:   string(keyPEM),
		IssuedTo:     name(leaf.Subject.CommonName, leaf.Subject.String()),
		IssuedBy:     name(leaf.Issuer.CommonName, leaf.Issuer.String()),
		IssuedOn:     leaf.NotBefore.UTC(),
		ValidUntil:   leaf.NotAfter.UTC(),
		Fingerprint:  strings.Join(pairs, ":"),
	}, nil
}

// name prefers a common name and falls back to the full distinguished name.
func name(cn, dn string) string {
	if cn != "" {
		return cn
	}
	return dn
}
