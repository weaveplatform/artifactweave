package work

import (
	"context"
	"time"
)

// LeaseStore persists named leases on background work.
type LeaseStore interface {
	// Acquire takes the lease l.Name for l.Holder until l.Expires when it is
	// free, expired at now, or already held by l.Holder (a renewal), and
	// reports whether it did. The stored lease is l as given, so a renewal
	// that wants to keep its acquisition time passes it back. ErrInvalid
	// when the name, holder or expiry is missing.
	Acquire(ctx context.Context, l Lease, now time.Time) (bool, error)
	// Release gives up a lease held by holder; releasing a lease someone
	// else holds, or nobody does, changes nothing.
	Release(ctx context.Context, name, holder string) error
	// List returns every lease ordered by name.
	List(ctx context.Context) ([]Lease, error)
}
