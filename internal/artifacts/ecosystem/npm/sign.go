package npm

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

// Signature is one entry of a version's dist.signatures, the shape 7.x's
// npm documentation shows and npm's pacote verifies (decision 0034,
// "Signatures").
type Signature struct {
	KeyID string `json:"keyid"`
	// Sig is the ASN.1 DER ECDSA signature in standard base64, which pacote
	// passes to Node's crypto.verify as 'base64'.
	Sig string `json:"sig"`
}

// Key is one entry of the keys document npm reads from <registry>/-/npm/v1/keys,
// with the fields and order of registry.npmjs.org's.
type Key struct {
	// Expires is always null: a 7.x key pair has no expiry, and pacote
	// treats null as a key that never expires [I] (#216).
	Expires *string `json:"expires"`
	KeyID   string  `json:"keyid"`
	// KeyType and Scheme are the key's SSH algorithm name, as npm's
	// registry gives them (ecdsa-sha2-nistp256 for a P-256 key).
	KeyType string `json:"keytype"`
	Scheme  string `json:"scheme"`
	// Key is the public key as base64 PKIX DER, which npm wraps in a PEM
	// "PUBLIC KEY" block to verify with.
	Key string `json:"key"`
}

// Signer signs a repository's versions with its primary key pair. A nil
// Signer signs nothing.
type Signer struct {
	key   *ecdsa.PrivateKey
	keyID string
}

// SignerFor is the signer of c: its primary key pair when that is an ECDSA
// pair and c is a local or federated repository, as 7.x signs local
// repositories only and a federated repository is a local one with members
// [I] (#216). It is nil otherwise: a remote repository relays its upstream's
// signatures, and a pair of another type signs nothing, since 7.x's
// repository page offers only ECDSA pairs for npm and the repositories API
// accepts any pair by name. Signing uses the primary pair alone: the
// secondary is listed by the keys endpoint so that versions signed before
// a rotation still verify until a reindex signs them again.
//
// It reads the pair through keys with ctx, so inside a transaction the
// read takes part in it. A nil keys signs nothing: a caller that builds a
// format environment without the key store runs no work that signs.
func SignerFor(ctx context.Context, keys accessapi.Keys, c repo.Config) (*Signer, error) {
	if keys == nil || !signs(c) {
		return nil, nil //nolint:nilnil // a nil Signer is signing off, not a failure
	}
	pair, ok, err := ecdsaPair(ctx, keys, c.PrimaryKeyPairRef)
	if err != nil || !ok {
		return nil, err
	}
	key, err := keymaterial.ECDSAPrivateKey(pair.PrivateKey, pair.Passphrase)
	if err != nil {
		return nil, fmt.Errorf("npm: signing key %s: %w", pair.Name, err)
	}
	pub, err := PublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("npm: signing key %s: %w", pair.Name, err)
	}
	return &Signer{key: key, keyID: pub.KeyID}, nil
}

// signs reports whether c is a repository whose versions a key pair signs.
func signs(c repo.Config) bool {
	return Serves(c.PackageType) && (c.Class == repo.Local || c.Class == repo.Federated)
}

// Sign is the dist.signatures of name's version with integrity, its
// dist.integrity: one signature over "<name>@<version>:<integrity>", the
// message pacote verifies with SHA-256. The signature is deterministic (RFC
// 6979), so a reindex with the same pair writes the same bytes, as decision
// 0034 has every reindex do. A nil Signer gives nil.
func (s *Signer) Sign(name, version, integrity string) ([]Signature, error) {
	if s == nil {
		return nil, nil
	}
	digest := sha256.Sum256([]byte(name + "@" + version + ":" + integrity))
	// A nil random source makes the signature deterministic.
	sig, err := s.key.Sign(nil, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("npm: sign %s@%s: %w", name, version, err)
	}
	return []Signature{{KeyID: s.keyID, Sig: base64.StdEncoding.EncodeToString(sig)}}, nil
}

// PublicKey is pub as the keys document names it. Its keyid is the key's
// OpenSSH SHA-256 fingerprint, which is how registry.npmjs.org derives the
// keyids it publishes; 7.x's documentation shows only "SHA256:" and a
// placeholder [I] (#216).
func PublicKey(pub *ecdsa.PublicKey) (Key, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return Key{}, fmt.Errorf("npm: public key: %w", err)
	}
	sshKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Key{}, fmt.Errorf("npm: public key: %w", err)
	}
	return Key{
		KeyID: ssh.FingerprintSHA256(sshKey), KeyType: sshKey.Type(), Scheme: sshKey.Type(),
		Key: base64.StdEncoding.EncodeToString(der),
	}, nil
}

// Keys is what the keys endpoint lists for c: the public keys of its
// primary and secondary pairs that are ECDSA, the primary first, each once.
// It is empty when c names no ECDSA pair, or is a repository that signs
// nothing, and the endpoint then answers as before signing existed. Listing
// the secondary lets a rotation stage the next pair there, so that the
// versions a reindex has not yet signed again still verify [I] (#216).
func Keys(ctx context.Context, keys accessapi.Keys, c repo.Config) ([]Key, error) {
	if keys == nil || !signs(c) {
		return nil, nil
	}
	var out []Key
	for _, name := range []string{c.PrimaryKeyPairRef, c.SecondaryKeyPairRef} {
		pair, ok, err := ecdsaPair(ctx, keys, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		pub, err := keymaterial.ECDSAPublicKey(pair.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("npm: public key of %s: %w", pair.Name, err)
		}
		k, err := PublicKey(pub)
		if err != nil {
			return nil, err
		}
		if len(out) == 0 || out[0].KeyID != k.KeyID {
			out = append(out, k)
		}
	}
	return out, nil
}

// ecdsaPair reads the pair called name, ok only when it exists and is
// ECDSA. A missing pair signs nothing: the repositories API refuses to name
// one, and deleting a pair clears every reference to it, so only a race
// finds none.
func ecdsaPair(
	ctx context.Context,
	keys accessapi.Keys,
	name string,
) (accessapi.Keypair, bool, error) {
	if name == "" {
		return accessapi.Keypair{}, false, nil
	}
	pair, err := keys.Keypair(ctx, name)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return accessapi.Keypair{}, false, nil
	case err != nil:
		return accessapi.Keypair{}, false, fmt.Errorf("npm: key pair %s: %w", name, err)
	}
	return pair, pair.Type == accessapi.KeypairECDSA, nil
}
