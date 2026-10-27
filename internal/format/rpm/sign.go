package rpm

import (
	"bytes"
	"crypto"
	"fmt"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// SignRepomd produces the armored detached signature dnf checks when
// repo_gpgcheck is enabled. It signs metadata, never the RPM archive itself.
func SignRepomd(body []byte, armoredPrivate, passphrase string) ([]byte, error) {
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armoredPrivate))
	if err != nil || len(ring) == 0 || ring[0].PrivateKey == nil {
		return nil, fmt.Errorf("%w: invalid gpg signing key", errInvalidPackage)
	}
	signer := ring[0]
	if signer.PrivateKey.Encrypted {
		if err := signer.DecryptPrivateKeys([]byte(passphrase)); err != nil {
			return nil, fmt.Errorf("rpm: unlock gpg key: %w", err)
		}
	}
	var signature bytes.Buffer
	if err := openpgp.ArmoredDetachSign(
		&signature, signer, bytes.NewReader(body),
		&packet.Config{DefaultHash: crypto.SHA256},
	); err != nil {
		return nil, fmt.Errorf("rpm: sign repomd: %w", err)
	}
	return signature.Bytes(), nil
}
