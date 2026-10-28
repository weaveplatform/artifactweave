package keymaterial

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
)

// Ed25519 is the signing key type used by Nix binary caches.
const Ed25519 = "Ed25519"

func Ed25519PrivateKey(privateKey, passphrase string) (ed25519.PrivateKey, error) {
	signer, err := parsePrivateKey([]byte(privateKey), passphrase)
	if err != nil {
		return nil, err
	}
	key, ok := signer.(ed25519.PrivateKey)
	if !ok {
		return nil, invalidf("the private key is not an Ed25519 key")
	}
	return key, nil
}

func Ed25519PublicKey(publicKey string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, invalidf("the public key is not Ed25519 PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, invalidf("the public key does not parse")
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, invalidf("the public key is not an Ed25519 key")
	}
	return key, nil
}

func checkEd25519(privateKey, publicKey, passphrase string) error {
	private, err := Ed25519PrivateKey(privateKey, passphrase)
	if err != nil {
		return err
	}
	public, err := Ed25519PublicKey(publicKey)
	if err != nil {
		return err
	}
	if !private.Public().(ed25519.PublicKey).Equal(public) {
		return invalidf("the private and public keys do not match")
	}
	return nil
}
