package artifactshttp

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

type repositoryRemoval struct {
	files int64
	found bool
}

// repositoryMutation retains current authority through planning and publication.
// Nested repository/source/configuration writes therefore take their locks only
// after the authority guard. No response escapes a failed outer commit.
func repositoryMutation[T any](ctx context.Context, s ArtifactoryAPI, fn func(context.Context) (T, error)) (T, error) {
	var zero, out T
	if _, err := kernel.RequireAuthenticated(ctx); err != nil {
		return zero, fmt.Errorf("repository caller: %w", err)
	}
	st, err := s.content()
	if err != nil {
		return zero, err
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		p, access, err := s.CurrentAccess(ctx)
		if err != nil {
			return fmt.Errorf("current repository administration: %w", err)
		}
		if p.Anonymous {
			return kernel.ErrUnauthenticated
		}
		ctx = accessapi.WithCaller(ctx, p, access.Decider)
		out, err = fn(ctx)
		return err
	})
	if err != nil {
		return zero, fmt.Errorf("repository administration transaction: %w", err)
	}
	return out, nil
}

func repositoryPermission(ctx context.Context, c repo.Config, operation accessapi.RepositoryOperation) error {
	p, err := httpkit.Principal(ctx)
	if err != nil {
		return err
	} //nolint:wrapcheck // retain authentication status
	d, _ := accessapi.DeciderFrom(ctx)
	if d == nil || !d.ManagesRepository(c.ProjectKey, string(c.Class), operation) {
		return httpkit.Denied(p)
	}
	return nil
}

// existingRepositoryPermission reads no sealed configuration before deciding
// whether this caller manages the current canonical owner/class. Missing keys
// retain the native administrator's 404; other callers cannot infer an owner.
func (s ArtifactoryAPI) existingRepositoryPermission(ctx context.Context, st Store, key string, operation accessapi.RepositoryOperation) error {
	info, err := st.Repos().ProjectRepository(ctx, key)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return s.InternalError(ctx, "repository authority context", err)
	}
	return repositoryPermission(ctx, repo.Config{Key: info.Key, Class: info.Class, ProjectKey: info.ProjectKey}, operation)
}

func createdRepositoryPermissions(ctx context.Context, configs []repo.Config) error {
	for _, c := range configs {
		if err := repositoryPermission(ctx, c, accessapi.RepositoryCreate); err != nil {
			return err
		}
	}
	for _, c := range configs {
		if err := repos.CheckProjectPrefix(c); err != nil {
			return fmt.Errorf("repository project prefix: %w", err)
		}
	}
	return nil
}
