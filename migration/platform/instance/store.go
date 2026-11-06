package instance

import "context"

// InstanceStore persists this instance's peer identity (decision 0021), a
// singleton created at first start. The private key is sealed.
type InstanceStore interface {
	// Init stores the identity; kernel.ErrConflict when one exists.
	Init(ctx context.Context, id InstanceIdentity) error
	// Get returns the identity; kernel.ErrNotFound before Init, which
	// EnsureInstance reads as a first start.
	Get(ctx context.Context) (InstanceIdentity, error)
	// Update replaces the identity, for key rotation; kernel.ErrNotFound
	// before Init.
	Update(ctx context.Context, id InstanceIdentity) error
}
