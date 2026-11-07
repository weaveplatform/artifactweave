package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runInstance covers this instance's identity (decision 0021).
func runInstance(t *testing.T, newStore Factory) {
	t.Run("Instance", func(t *testing.T) { testInstance(t, newStore(t)) })
}

func testInstance(t *testing.T, s store.Store) {
	ctx := context.Background()
	is := s.Instance()
	_, err := is.Get(ctx)
	storekit.WantErr(t, err, store.ErrNotFound)
	pub, priv := peerKey(1)
	id := types.InstanceIdentity{
		InstanceID: peerID(1), PublicKey: pub, PrivateKey: priv,
		NamespaceID: "ns-1", ProtocolVersion: types.ProtocolVersion,
	}
	storekit.WantErr(t, is.Update(ctx, id), store.ErrNotFound)
	storekit.Must(t, is.Init(ctx, id))
	got := storekit.Get[types.InstanceIdentity](t)(is.Get(ctx))
	storekit.Same(t, "Get", id, got)

	// Returned keys are copies.
	got.PublicKey[0]++
	got.PrivateKey[0]++
	storekit.Same(
		t,
		"Get after mutating a copy",
		id,
		storekit.Get[types.InstanceIdentity](t)(is.Get(ctx)),
	)

	second := id
	second.InstanceID = peerID(2)
	storekit.WantErr(t, is.Init(ctx, second), store.ErrConflict)

	// Key rotation, and an identity with no private key at all.
	pub2, priv2 := peerKey(2)
	rotated := types.InstanceIdentity{
		InstanceID: peerID(
			2,
		),
		PublicKey:       pub2,
		PrivateKey:      priv2,
		NamespaceID:     "ns-2",
		ProtocolVersion: 2,
	}
	storekit.Must(t, is.Update(ctx, rotated))
	storekit.Same(
		t,
		"Get after Update",
		rotated,
		storekit.Get[types.InstanceIdentity](t)(is.Get(ctx)),
	)
	bare := types.InstanceIdentity{InstanceID: peerID(3)}
	storekit.Must(t, is.Update(ctx, bare))
	storekit.Same(t, "Get with no keys", bare, storekit.Get[types.InstanceIdentity](t)(is.Get(ctx)))
	failsCancelled(t, map[string]func(context.Context) error{
		"Get":    func(ctx context.Context) error { _, err := is.Get(ctx); return err },
		"Init":   func(ctx context.Context) error { return is.Init(ctx, id) },
		"Update": func(ctx context.Context) error { return is.Update(ctx, id) },
	})
}
