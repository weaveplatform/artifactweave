package sql

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ port.PeerRevisions = peerStore{}

func (p peerStore) Revision(ctx context.Context, id string) (port.PeerRevision, error) {
	var out port.PeerRevision
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT grant_revision, failure_revision FROM peers WHERE instance_id = ?"), id).
		Scan(&out.Grant, &out.Failure)
	return out, p.s.Wrap("read peer observation revision", err)
}

func (p peerStore) GuardRevision(ctx context.Context, id string, expected port.PeerRevision) error {
	if !p.s.InTx(ctx) || expected.Grant == "" || expected.Failure == "" {
		return fmt.Errorf("peer observation guard requires a transaction and revision: %w", kernel.ErrInvalid)
	}
	// The conditional write acquires the same row lock as replacement or
	// revocation on both SQL backends, including SQLite's deferred Tx mode.
	n, err := p.s.Exec(ctx, "guard peer observation revision",
		"UPDATE peers SET grant_revision = grant_revision WHERE instance_id = ? AND grant_revision = ? AND failure_revision = ?",
		id, expected.Grant, expected.Failure)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("peer observation changed: %w", kernel.ErrConflict)
	}
	return nil
}

func (p peerStore) ObserveFailure(ctx context.Context, id, grant string) error {
	if grant == "" {
		return fmt.Errorf("peer observation has no grant revision: %w", kernel.ErrInvalid)
	}
	n, err := p.s.Exec(ctx, "invalidate failed peer observations",
		"UPDATE peers SET failure_revision = ? WHERE instance_id = ? AND grant_revision = ?", rand.Text(), id, grant)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("failed observation belongs to an old peer grant: %w", kernel.ErrConflict)
	}
	return nil
}
