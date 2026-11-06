package access

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

var _ accessapi.Keys = keys{}

// keys reads key pairs and certificates from the key store, which unseals
// them. Errors are the store's, unwrapped, so errors.Is(err,
// kernel.ErrNotFound) and each caller's own wrap still apply.
type keys struct{ st store.Store }

func (b keys) Keypair(ctx context.Context, name string) (accessapi.Keypair, error) {
	//nolint:wrapcheck // the store's errors are the contract's
	return b.st.Keys().Keypair(ctx, name)
}

func (b keys) Certificate(ctx context.Context, alias string) (accessapi.Certificate, error) {
	//nolint:wrapcheck // the store's errors are the contract's
	return b.st.Keys().Certificate(ctx, alias)
}
