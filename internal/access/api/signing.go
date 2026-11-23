package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// DefaultGPGKey names the instance-wide GPG key pair, 7.161.26's
// KeyPairDescriptor.DEFAULT_GPG_KEY. The /api/gpg operations set its halves
// and passphrase one at a time, and the key pair API lists it like any
// other pair.
const DefaultGPGKey = "default-gpg-key"

var errNoKeyStore = errors.New("no key store")

// MetadataSigningPair returns the pair a Debian, RPM or Opkg repository
// signs its metadata with, and whether one does.
//
// With ref set, that is the pair ref names, whatever its type, with the
// read's errors (one matching kernel.ErrNotFound when it does not exist):
// the caller refuses a pair of another type, as it did before.
//
// With ref empty, it is the instance pair (DefaultGPGKey), chosen when the
// metadata is built rather than when the repository is saved [I]. The
// instance pair signs only once both halves are set, the passphrase
// unlocks the private one and the two belong together. Until then nothing
// signs, as before the pair was set: its halves are written one request at
// a time, and a half-written key must not fail every repository that named
// no key [I]. keys may be nil, as for an index built without a key store;
// then only ref can sign.
func MetadataSigningPair(ctx context.Context, keys Keys, ref string) (Keypair, bool, error) {
	if ref != "" {
		if keys == nil {
			return Keypair{}, false, fmt.Errorf("signing key %q: %w", ref, errNoKeyStore)
		}
		k, err := keys.Keypair(ctx, ref)
		if err != nil {
			return Keypair{}, false, err //nolint:wrapcheck // each signer names its own key
		}
		return k, true, nil
	}
	if keys == nil {
		return Keypair{}, false, nil
	}
	k, err := keys.Keypair(ctx, DefaultGPGKey)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return Keypair{}, false, nil
	case err != nil:
		return Keypair{}, false, fmt.Errorf("instance GPG key: %w", err)
	case !instanceSigns(k):
		return Keypair{}, false, nil
	}
	return k, true, nil
}

// instanceSigns reports whether the instance pair k can sign: a GPG pair
// whose private half the passphrase unlocks and whose public half carries
// the same primary key. The public half passed the complete-field public
// check when it was set (setGpgPublicKey).
func instanceSigns(k Keypair) bool {
	if k.Type != KeypairGPG || k.PrivateKey == "" || k.PublicKey == "" {
		return false
	}
	secret, err := openpgp.ReadArmoredKeyRing(strings.NewReader(k.PrivateKey))
	if err != nil || len(secret) != 1 || secret[0].PrivateKey == nil {
		return false
	}
	if secret[0].PrivateKey.Encrypted && secret[0].DecryptPrivateKeys([]byte(k.Passphrase)) != nil {
		return false
	}
	public, err := openpgp.ReadArmoredKeyRing(strings.NewReader(k.PublicKey))
	if err != nil || len(public) != 1 {
		return false
	}
	return bytes.Equal(secret[0].PrimaryKey.Fingerprint, public[0].PrimaryKey.Fingerprint)
}
