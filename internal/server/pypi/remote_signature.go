package pypi

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Parsing prevents a successful HTML error or malformed packet from being
// admitted as a detached signature. Trust and signature verification belong to
// the consumer; the proxy delivers the exact upstream bytes.
func validateRemoteSignature(_ context.Context, r io.ReadSeeker, size int64) error {
	if size > 1<<20 {
		return remoteError()
	}
	b, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return remoteError()
	}
	var packets io.Reader = bytes.NewReader(b)
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("-----BEGIN")) {
		if !bytes.HasSuffix(bytes.TrimSpace(b), []byte("-----END PGP SIGNATURE-----")) {
			return remoteError()
		}
		block, e := armor.Decode(packets)
		if e != nil || block.Type != "PGP SIGNATURE" {
			return remoteError()
		}
		packets = block.Body
	}
	count := 0
	for {
		p, e := packet.Read(packets)
		if errors.Is(e, io.EOF) && count > 0 {
			return nil
		}
		if e != nil || count >= 32 {
			return remoteError()
		}
		if _, ok := p.(*packet.Signature); !ok {
			return remoteError()
		}
		count++
	}
}
