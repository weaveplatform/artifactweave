package instance

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// InstanceStore persists this instance's peer identity (decision 0021), a
// singleton created at first start. The private key is sealed.
type InstanceStore interface {
	// Init stores the identity; ErrConflict when one exists.
	Init(ctx context.Context, id types.InstanceIdentity) error
	// Get returns the identity.
	Get(ctx context.Context) (types.InstanceIdentity, error)
	// Update replaces the identity, for key rotation.
	Update(ctx context.Context, id types.InstanceIdentity) error
}
