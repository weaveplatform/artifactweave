package upstreamtest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
)

// NewPool returns an empty transport pool that is closed when t ends.
func NewPool(t testing.TB) *upstream.Pool {
	t.Helper()
	p := new(upstream.Pool)
	t.Cleanup(p.Close)
	return p
}

// NewResolver returns a credential resolver over st with no keyring and the
// given revision key (at least 32 bytes); it fails t when the options are invalid.
func NewResolver(t testing.TB, st upstream.ResolverStore, revisionKey []byte) *upstream.Resolver {
	t.Helper()
	r, err := upstream.NewResolver(st, nil, upstream.ResolverOptions{RevisionKey: revisionKey})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
