package keymaterial

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"strings"

	pgpecdsa "github.com/ProtonMail/go-crypto/openpgp/ecdsa"
)

// ECDSA is the signing key type of npm package signatures (decision 0034,
// "Signatures"). 7.x's Keys Management names it "ECDSA Keys"; the security
// API's documentation lists only RSA and GPG, so the REST spelling is
// inferred [I] (#216).
const ECDSA = "ECDSA"

// ecdsaCurves are the curves an ECDSA pair may use: those npm's keys
// endpoint can name (ecdsa-sha2-nistp256, -384 and -521) and that Go signs
// deterministically (RFC 6979), so a reindex writes the same signature.
var ecdsaCurves = map[string]elliptic.Curve{
	"P-256": elliptic.P256(), "P-384": elliptic.P384(), "P-521": elliptic.P521(),
}

// armoredOpenPGP reports whether s is an ASCII-armored OpenPGP block. 7.x's
// npm documentation has users generate the ECDSA pair with gpg and upload
// its armored export, so both halves may come in that form as well as PEM.
func armoredOpenPGP(s string) bool {
	return strings.Contains(s, "-----BEGIN PGP ")
}

// ECDSAPrivateKey opens the private half of an ECDSA key pair, so that a
// format can sign with it: a PEM key (PKCS#8 or SEC 1, optionally encrypted
// the legacy OpenSSL way) or an ASCII-armored OpenPGP secret key whose
// primary key is ECDSA, unlocked with passphrase when it is encrypted. The
// error wraps ErrInvalid when the key does not parse, the passphrase does
// not unlock it, it is another kind of key, or its curve is not P-256,
// P-384 or P-521.
func ECDSAPrivateKey(privateKey, passphrase string) (*ecdsa.PrivateKey, error) {
	if armoredOpenPGP(privateKey) {
		return openPGPECDSAPrivate(privateKey, passphrase)
	}
	signer, err := parsePrivateKey([]byte(privateKey), passphrase)
	if err != nil {
		return nil, err
	}
	key, ok := signer.(*ecdsa.PrivateKey)
	if !ok {
		return nil, invalidf("the private key is not an ECDSA key")
	}
	if err := checkCurve(key.Curve); err != nil {
		return nil, err
	}
	return key, nil
}

// ECDSAPublicKey reads the public half of an ECDSA key pair: a PKIX "PUBLIC
// KEY" PEM, a certificate, or an ASCII-armored OpenPGP public key whose
// primary key is ECDSA. The error wraps ErrInvalid as ECDSAPrivateKey's
// does.
func ECDSAPublicKey(publicKey string) (*ecdsa.PublicKey, error) {
	if armoredOpenPGP(publicKey) {
		return openPGPECDSAPublic(publicKey)
	}
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil {
		return nil, invalidf("the public key is not PEM")
	}
	var key any
	var err error
	switch block.Type {
	case "PUBLIC KEY":
		key, err = x509.ParsePKIXPublicKey(block.Bytes)
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
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, invalidf("the public key is not an ECDSA key")
	}
	if err := checkCurve(pub.Curve); err != nil {
		return nil, err
	}
	return pub, nil
}

func checkCurve(c elliptic.Curve) error {
	if _, ok := ecdsaCurves[c.Params().Name]; !ok {
		return invalidf("the ECDSA key's curve must be P-256, P-384 or P-521, not %s",
			c.Params().Name)
	}
	return nil
}

func checkECDSA(privateKey, publicKey, passphrase string) error {
	private, err := ECDSAPrivateKey(privateKey, passphrase)
	if err != nil {
		return err
	}
	public, err := ECDSAPublicKey(publicKey)
	if err != nil {
		return err
	}
	if !private.PublicKey.Equal(public) {
		return invalidf("the private and public keys do not match")
	}
	return nil
}

// openPGPECDSAPrivate is the ECDSA key an armored OpenPGP secret key holds
// as its primary key, which is what gpg's "ECC (sign only)" generates.
func openPGPECDSAPrivate(armored, passphrase string) (*ecdsa.PrivateKey, error) {
	secret, err := readEntity(armored, "private")
	if err != nil {
		return nil, err
	}
	if secret.PrivateKey == nil {
		return nil, invalidf("the private key block holds no private key")
	}
	if secret.PrivateKey.Encrypted {
		if err := secret.PrivateKey.Decrypt([]byte(passphrase)); err != nil {
			return nil, invalidf("the passphrase does not unlock the private key")
		}
	}
	k, ok := secret.PrivateKey.PrivateKey.(*pgpecdsa.PrivateKey)
	if !ok {
		return nil, invalidf("the private key is not an ECDSA key")
	}
	curve, err := pgpCurve(&k.PublicKey)
	if err != nil {
		return nil, err
	}
	size := (curve.Params().BitSize + 7) / 8
	key, err := ecdsa.ParseRawPrivateKey(curve, k.D.FillBytes(make([]byte, size)))
	if err != nil {
		return nil, invalidf("the private key does not parse: %v", err)
	}
	return key, nil
}

// openPGPECDSAPublic is the ECDSA key an armored OpenPGP public key holds as
// its primary key.
func openPGPECDSAPublic(armored string) (*ecdsa.PublicKey, error) {
	public, err := readEntity(armored, "public")
	if err != nil {
		return nil, err
	}
	k, ok := public.PrimaryKey.PublicKey.(*pgpecdsa.PublicKey)
	if !ok {
		return nil, invalidf("the public key is not an ECDSA key")
	}
	curve, err := pgpCurve(k)
	if err != nil {
		return nil, err
	}
	size := (curve.Params().BitSize + 7) / 8
	point := append([]byte{4}, k.X.FillBytes(make([]byte, size))...)
	point = append(point, k.Y.FillBytes(make([]byte, size))...)
	key, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, invalidf("the public key does not parse: %v", err)
	}
	return key, nil
}

// pgpCurve is the standard library's curve of an OpenPGP ECDSA key, which
// go-crypto names as elliptic.Curve does for the NIST curves.
func pgpCurve(k *pgpecdsa.PublicKey) (elliptic.Curve, error) {
	name := k.GetCurve().GetCurveName()
	curve, ok := ecdsaCurves[name]
	if !ok {
		return nil, invalidf("the ECDSA key's curve must be P-256, P-384 or P-521, not %s", name)
	}
	return curve, nil
}
