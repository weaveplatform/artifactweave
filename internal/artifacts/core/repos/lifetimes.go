package repos

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// NewRepositoryLifetimes binds secret-free lifetime reads before module assembly.
func NewRepositoryLifetimes(store interface{ RemoteCache() port.RemoteCacheStore }) api.RepositoryLifetimes {
	return repositoryLifetimes{store: store}
}

type repositoryLifetimes struct {
	store interface{ RemoteCache() port.RemoteCacheStore }
}

func (r repositoryLifetimes) RepositoryIncarnation(ctx context.Context, key string) (string, error) {
	identity, err := r.store.RemoteCache().Source(ctx, key)
	if err != nil {
		return "", fmt.Errorf("repository lifetime: %w", err)
	}
	return string(identity.Repository), nil
}
