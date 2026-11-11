package debian

import (
	"bytes"
	"crypto"
	"fmt"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// SignedRelease is the two signatures apt can verify beside Release.
type SignedRelease struct {
	InRelease  []byte
	ReleaseGPG []byte
}

// SignRelease binds an apt Release file to the repository's configured GPG
// private key. Callers publish all three files in one derivation cycle.
func SignRelease(body []byte, armoredPrivate, passphrase string) (SignedRelease, error) {
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armoredPrivate))
	if err != nil || len(ring) == 0 || ring[0].PrivateKey == nil {
		return SignedRelease{}, fmt.Errorf("%w: invalid gpg signing key", errInvalidBinary)
	}
	signer := ring[0]
	if signer.PrivateKey.Encrypted {
		if err := signer.DecryptPrivateKeys([]byte(passphrase)); err != nil {
			return SignedRelease{}, fmt.Errorf("debian: unlock gpg signing key: %w", err)
		}
	}
	config := &packet.Config{DefaultHash: crypto.SHA256}
	var detached bytes.Buffer
	if err := openpgp.ArmoredDetachSign(
		&detached,
		signer,
		bytes.NewReader(body),
		config,
	); err != nil {
		return SignedRelease{}, fmt.Errorf("debian: sign Release: %w", err)
	}
	var inline bytes.Buffer
	w, err := clearsign.Encode(&inline, signer.PrivateKey, config)
	if err != nil {
		return SignedRelease{}, fmt.Errorf("debian: begin InRelease: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return SignedRelease{}, fmt.Errorf("debian: write InRelease: %w", err)
	}
	if err := w.Close(); err != nil {
		return SignedRelease{}, fmt.Errorf("debian: finish InRelease: %w", err)
	}
	return SignedRelease{InRelease: inline.Bytes(), ReleaseGPG: detached.Bytes()}, nil
}
