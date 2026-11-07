package instance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// Store is what EnsureInstance needs from persistence: the identity store,
// which it reads and, on first start, writes. The metadata store satisfies
// it, so the composition root passes its store as it is.
type Store interface {
	Instance() InstanceStore
}

// EnsureInstance creates this instance's identity on first start: the
// jfrt@<uuid> id Artifactory reports as its service id, from which the
// Access issuer jfac@<uuid> is derived, and the Ed25519 key peers know it by
// (decision 0021). Two instances starting together on one database agree on
// whichever identity was stored first.
//
// It creates an identity only when Get reports kernel.ErrNotFound, and
// leaves a stored one as it is, because tokens issued before a restart carry
// an issuer derived from it. It opens no transaction: when Init answers
// kernel.ErrConflict, another instance stored its identity between the Get
// and the Init, and EnsureInstance returns nil. Any other error from the
// store, or a key that cannot be generated, is returned wrapped, its text
// starting "instance identity:".
func EnsureInstance(ctx context.Context, st Store) error {
	_, err := st.Instance().Get(ctx)
	if !errors.Is(err, kernel.ErrNotFound) {
		if err != nil {
			return fmt.Errorf("instance identity: %w", err)
		}
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("instance identity: %w", err)
	}
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	err = st.Instance().Init(ctx, InstanceIdentity{
		InstanceID: fmt.Sprintf(
			"jfrt@%x-%x-%x-%x-%x",
			u[0:4],
			u[4:6],
			u[6:8],
			u[8:10],
			u[10:],
		),
		PublicKey:       pub,
		PrivateKey:      priv,
		ProtocolVersion: ProtocolVersion,
	})
	if err != nil && !errors.Is(err, kernel.ErrConflict) {
		return fmt.Errorf("instance identity: %w", err)
	}
	return nil
}
