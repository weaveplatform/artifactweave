package access

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ accessapi.Projects = projects{}

// projects reads project definitions and coordinates repository bindings
// (decision 0013). Reads preserve store errors; binding writes add operation
// context and share the caller's guarded transaction. errors.Is still finds
// the store's sentinels through each caller's wrapping.
type projects struct{ st port.Store }

func (b projects) Guard(ctx context.Context) error {
	return b.st.Projects().Guard(ctx) //nolint:wrapcheck // preserve the shared guard failure
}

func (b projects) BindRepository(ctx context.Context, key, project string, environments []string, fresh bool) error {
	return b.st.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // binding steps preserve their own error context
		if err := b.Guard(ctx); err != nil {
			return err
		}
		a := model.RepositoryAssignment{}
		if !fresh {
			var err error
			a, err = b.st.Projects().Assignment(ctx, key)
			if err != nil && !errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("read repository binding: %w", err)
			}
		}
		a.RepoKey, a.ProjectKey, a.Environments, a.Updated = key, project, slices.Clone(environments), time.Now().UTC()
		if err := b.st.Projects().PutAssignment(ctx, a); err != nil {
			return fmt.Errorf("publish repository binding: %w", err)
		}
		return nil
	})
}

func (b projects) RemoveRepository(ctx context.Context, key string) error {
	return b.st.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // binding steps preserve their own error context
		if err := b.Guard(ctx); err != nil {
			return err
		}
		// A missing binding is valid for a repository predating this lifecycle.
		// Detect it before DeleteAssignment's nested transaction can mark the
		// enclosing SQL transaction for rollback. The guard keeps this read
		// and the removal in the same authority snapshot.
		if _, err := b.st.Projects().Assignment(ctx, key); err != nil {
			if errors.Is(err, kernel.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("read repository binding: %w", err)
		}
		if err := b.st.Projects().DeleteAssignment(ctx, key); err != nil {
			return fmt.Errorf("remove repository binding: %w", err)
		}
		return nil
	})
}

func (b projects) Environments(ctx context.Context, project string) ([]string, error) {
	envs, err := b.st.Projects().Environments(ctx, project)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store's errors are the contract's
	}
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Name)
	}
	return names, nil
}

func (b projects) Member(ctx context.Context, project, user string) error {
	_, err := b.st.Projects().Member(ctx, project, model.PrincipalUser, user)
	return err //nolint:wrapcheck // the store's errors are the contract's
}

func (b projects) Exists(ctx context.Context, project string) error {
	_, err := b.st.Projects().Get(ctx, project)
	return err //nolint:wrapcheck // the store's errors are the contract's
}
