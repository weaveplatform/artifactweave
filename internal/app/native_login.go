package app

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/session"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// nativeRepository reads only inside access's administration transaction. The
// lifetime and owner prevent a pending approval adopting a recreated/transferred
// key. Access separately evaluates the current authenticated user's visibility.
func nativeRepository(st interface {
	Repos() artport.RepoStore
	RemoteCache() artport.RemoteCacheStore
}) session.Repository {
	return func(ctx context.Context, key string) (accessapi.NativeRepository, error) {
		cfg, err := st.Repos().Get(ctx, key)
		if err != nil {
			return accessapi.NativeRepository{}, fmt.Errorf("native repository: %w", err)
		}
		if cfg.PackageType != repo.NPM {
			return accessapi.NativeRepository{}, kernel.ErrNotFound
		}
		inc, err := repos.NewRepositoryLifetimes(st).RepositoryIncarnation(ctx, key)
		if err != nil {
			return accessapi.NativeRepository{}, fmt.Errorf("native repository lifetime: %w", err)
		}
		return accessapi.NativeRepository{Key: cfg.Key, Class: string(cfg.Class), Project: cfg.ProjectKey, Incarnation: inc}, nil
	}
}
