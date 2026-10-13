package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tokenKeyBits is the access-token RSA key size; zero means the production
// size. Tests set a small one so every runtime they open stays cheap.
var tokenKeyBits int

// ensureInstance creates this instance's identity on first start: the
// jfrt@<uuid> id Artifactory reports as its service id, from which the
// Access issuer jfac@<uuid> is derived, and the Ed25519 key peers know it by
// (decision 0021). Two instances starting together on one database agree on
// whichever identity was stored first.
func ensureInstance(ctx context.Context, st store.Store) error {
	_, err := st.Instance().Get(ctx)
	if !errors.Is(err, store.ErrNotFound) {
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
	err = st.Instance().Init(ctx, types.InstanceIdentity{
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
		ProtocolVersion: types.ProtocolVersion,
	})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("instance identity: %w", err)
	}
	return nil
}

// openTokens creates the instance identity when missing and the token
// service over it, which creates the first signing key.
func openTokens(ctx context.Context, st store.Store) (*token.Service, error) {
	if err := ensureInstance(ctx, st); err != nil {
		return nil, err
	}
	tokens, err := token.New(ctx, st, token.Options{KeyBits: tokenKeyBits})
	if err != nil {
		return nil, fmt.Errorf("access tokens: %w", err)
	}
	return tokens, nil
}
