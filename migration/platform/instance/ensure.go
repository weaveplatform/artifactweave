package instance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

type Store interface {
	Instance() InstanceStore
}

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
