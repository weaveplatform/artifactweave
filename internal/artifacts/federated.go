package artifacts

import (
	"context"
	"fmt"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// federatedItems is FederatedItems over the module's item service, so a
// member's change runs the same formats, derived work and package index
// as a client's.
type federatedItems struct {
	store Store
	items *item.Service
}

// repository reads key as it is now, refusing one that is not federated:
// a change of a federated repository's stream applies to nothing else,
// whatever a configuration changed meanwhile.
func (f federatedItems) repository(ctx context.Context, key string) (repo.Config, error) {
	c, err := f.store.Repos().Get(ctx, key)
	if err != nil {
		return repo.Config{}, fmt.Errorf("federated repository %s: %w", key, err)
	}
	if c.Class != repo.Federated {
		return repo.Config{}, fmt.Errorf("repository %s is not federated: %w", key, kernel.ErrNotFound)
	}
	return c, nil
}

func (f federatedItems) Put(ctx context.Context, c artifactsapi.ItemChange) error {
	cfg, err := f.repository(ctx, c.Repository)
	if err != nil {
		return err
	}
	return f.items.ApplyFederated(ctx, cfg, c) //nolint:wrapcheck // the item service names the item
}

func (f federatedItems) Delete(ctx context.Context, repoKey, path string, folder bool) error {
	cfg, err := f.repository(ctx, repoKey)
	if err != nil {
		return err
	}
	return f.items.DeleteFederated(ctx, cfg, path, folder) //nolint:wrapcheck // the item service names the item
}

func (f federatedItems) Discard(ctx context.Context, repoKey, path string) error {
	cfg, err := f.repository(ctx, repoKey)
	if err != nil {
		return err
	}
	return f.items.DiscardFederated(ctx, cfg, path) //nolint:wrapcheck // the item service names the item
}

func (f federatedItems) List(
	ctx context.Context, repoKey, after string, limit int, properties bool,
) ([]artifactsapi.ItemChange, error) {
	cfg, err := f.repository(ctx, repoKey)
	if err != nil {
		return nil, err
	}
	return f.items.ListFederated(ctx, cfg, after, limit, properties) //nolint:wrapcheck // the item service names the list
}

func (f federatedItems) Holds(ctx context.Context, repoKey, sha256 string) (bool, error) {
	cfg, err := f.repository(ctx, repoKey)
	if err != nil {
		return false, err
	}
	return f.items.HoldsFederated(ctx, cfg, sha256) //nolint:wrapcheck // the item service names the lookup
}

// NewFederationStatusItemReader supplies read-only item state for monitoring,
// including the local and remote-cache repositories the legacy endpoint accepts.
func NewFederationStatusItemReader(store Store) artifactsapi.FederatedItemReader {
	return federationStatusItems{store: store, items: &item.Service{Store: store}}
}

type federationStatusItems struct {
	store Store
	items *item.Service
}

func (f federationStatusItems) List(ctx context.Context, key, after string, limit int, properties bool) ([]artifactsapi.ItemChange, error) {
	stored, cached := repo.ResolveKey(key)
	cfg, err := f.store.Repos().Get(ctx, stored)
	if err != nil {
		return nil, fmt.Errorf("status repository %s: %w", key, err)
	}
	return f.items.ListRepositoryItems(ctx, cfg, cached, after, limit, properties) //nolint:wrapcheck // the item service names the read
}
