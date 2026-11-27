package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func testPeerRevisions(t *testing.T, s port.Store) {
	t.Helper()
	ctx := t.Context()
	peers := s.Peers()
	revisions, ok := peers.(port.PeerRevisions)
	if !ok {
		t.Fatal("peer store has no current observation fence")
	}
	p := peer(1, model.UseFederatedRepo)
	storekit.Must(t, peers.Put(ctx, p))
	first := storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID))
	if first.Grant == "" || first.Failure == "" {
		t.Fatal("a grant has no durable observation identity")
	}
	storekit.WantErr(t, revisions.GuardRevision(ctx, p.InstanceID, first), kernel.ErrInvalid)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return revisions.GuardRevision(ctx, p.InstanceID, first) }))
	storekit.Must(t, peers.SetLiveness(ctx, p.InstanceID, model.Liveness{Status: model.PeerOnline, LastSeen: storekit.At(9)}))
	storekit.Must(t, peers.SetProtocolVersion(ctx, p.InstanceID, instance.ProtocolVersion))
	storekit.Same(t, "liveness preserves grant and failure identities", first, storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID)))
	storekit.Must(t, peers.SetLiveness(ctx, p.InstanceID, model.Liveness{Status: model.PeerOffline}))
	failed := storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID))
	if failed.Grant != first.Grant || failed.Failure == first.Failure {
		t.Fatal("an observed failure did not invalidate existing observations")
	}
	storekit.Must(t, peers.SetLiveness(ctx, p.InstanceID, model.Liveness{Status: model.PeerOnline}))
	storekit.Same(t, "a later success cannot revive old observations", failed, storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID)))
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return revisions.GuardRevision(ctx, p.InstanceID, first) }), kernel.ErrConflict)
	storekit.Must(t, peers.Put(ctx, p))
	replaced := storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID))
	if replaced.Grant == first.Grant {
		t.Fatal("replacing the same key and use cases revived old grant authority")
	}
	storekit.WantErr(t, revisions.ObserveFailure(ctx, p.InstanceID, first.Grant), kernel.ErrConflict)
	storekit.Same(t, "an old request cannot poison its replacement", replaced, storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID)))
	storekit.Must(t, revisions.ObserveFailure(ctx, p.InstanceID, replaced.Grant))
	latest := storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID))
	if latest.Grant != replaced.Grant || latest.Failure == replaced.Failure {
		t.Fatal("failed cleanup collection did not invalidate the current observation generation")
	}
	rollback := errPeerRevisionRollback
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error {
		if err := peers.Put(ctx, p); err != nil {
			return fmt.Errorf("write rollback peer revision: %w", err)
		}
		return rollback
	}), rollback)
	storekit.Same(t, "rollback preserves the complete peer observation fence", latest, storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID)))
	storekit.Must(t, peers.Delete(ctx, p.InstanceID))
	storekit.Must(t, peers.Put(ctx, p))
	recreated := storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID))
	if recreated.Grant == latest.Grant {
		t.Fatal("revoke/re-pair adopted the old grant generation")
	}
	storekit.Must(t, peers.SetProtocolVersion(ctx, p.InstanceID, instance.ProtocolVersion+1))
	incompatible := storekit.Get[port.PeerRevision](t)(revisions.Revision(ctx, p.InstanceID))
	if incompatible.Grant != recreated.Grant || incompatible.Failure == recreated.Failure {
		t.Fatal("a protocol mismatch retained apparently current evidence")
	}
	failsCancelled(t, map[string]func(context.Context) error{
		"Revision": func(ctx context.Context) error {
			_, err := revisions.Revision(ctx, p.InstanceID)
			return revisionError(err)
		},
		"ObserveFailure": func(ctx context.Context) error {
			return revisions.ObserveFailure(ctx, p.InstanceID, incompatible.Grant)
		},
	})
}

var errPeerRevisionRollback = errors.New("rollback peer observation changes")

func revisionError(err error) error {
	if err != nil {
		return fmt.Errorf("read peer revision: %w", err)
	}
	return nil
}
