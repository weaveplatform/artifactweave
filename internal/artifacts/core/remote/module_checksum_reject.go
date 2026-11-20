package remote

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (p *Policy) fenceModuleChecksumRejection(q source.Request, permit source.Permit, revision string, reject func(context.Context) error) func(context.Context) error {
	if reject == nil {
		return nil
	}
	return func(ctx context.Context) error {
		return p.Store.Tx(ctx, func(tx context.Context) error {
			if err := p.Check(tx, q, permit, revision); err != nil {
				return err
			}
			return reject(tx)
		})
	}
}

func (p *Policy) rejectModuleChecksum(token model.RemoteCacheToken) func(context.Context) error {
	return func(ctx context.Context) error {
		// Read's enclosing transaction rechecks current policy and credentials.
		// Cached content may predate a policy-only edit; preserve its exact
		// source/epoch/revision while using that checked current policy revision.
		state, err := p.Store.RemoteCache().State(ctx, token.Key.RepoKey)
		if err != nil {
			return fmt.Errorf("read checksum rejection authority: %w", err)
		}
		expected := token
		expected.PolicyRevision = state.PolicyRevision
		err = p.Store.RemoteCache().Evict(ctx, expected)
		if errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("forget failed module checksum decision: %w", err)
		}
		return nil
	}
}
