package inmem

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var _ port.PeerRevisions = peerStore{}

func (p peerStore) revisions() *memtx.Table[string, port.PeerRevision] {
	return memtx.TableOf[string, port.PeerRevision](p.s.Store, "peer_observation_revisions")
}

func (p peerStore) Revision(ctx context.Context, id string) (port.PeerRevision, error) {
	var out port.PeerRevision
	err := p.s.Do(ctx, func() error {
		if !p.t().Has(id) {
			return memtx.NotFound("peer observation revision", id)
		}
		out, _ = p.revisions().Get(id)
		return nil
	})
	return out, err
}

func (p peerStore) GuardRevision(ctx context.Context, id string, expected port.PeerRevision) error {
	if !p.s.InTx(ctx) || expected.Grant == "" || expected.Failure == "" {
		return fmt.Errorf("peer observation guard requires a transaction and revision: %w", kernel.ErrInvalid)
	}
	actual, err := p.Revision(ctx, id)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("peer observation changed: %w", kernel.ErrConflict)
	}
	return nil
}

func (p peerStore) ObserveFailure(ctx context.Context, id, grant string) error {
	if grant == "" {
		return fmt.Errorf("peer observation has no grant revision: %w", kernel.ErrInvalid)
	}
	return p.s.Do(ctx, func() error {
		current, ok := p.revisions().Get(id)
		if !ok || !p.t().Has(id) || current.Grant != grant {
			return fmt.Errorf("failed observation belongs to an old peer grant: %w", kernel.ErrConflict)
		}
		current.Failure = rand.Text()
		p.revisions().Put(id, current)
		return nil
	})
}
