package app

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// tokenKeyBits is the access-token RSA key size; zero means the production
// size. Tests set a small one so every runtime they open stays cheap.
var tokenKeyBits int

// openTokens creates the instance identity when missing and the token
// service over it, which creates the first signing key.
func openTokens(ctx context.Context, st store.Store) (*token.Service, error) {
	if err := instance.EnsureInstance(ctx, st); err != nil {
		return nil, err
	}
	tokens, err := token.New(ctx, st, token.Options{KeyBits: tokenKeyBits})
	if err != nil {
		return nil, fmt.Errorf("access tokens: %w", err)
	}
	return tokens, nil
}
