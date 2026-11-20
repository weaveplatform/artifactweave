package project

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RepositoryStatus returns the canonical repository binding and its stored
// shares in one guarded snapshot. The caller authorizes status administration
// before invoking it; an absent assignment does not make an existing repository
// disappear from the status API.
func (s Service) RepositoryStatus(ctx context.Context, key string) (model.RepositoryAssignment, error) {
	var out model.RepositoryAssignment
	err := s.repositoryTransaction(ctx, key, func(_ context.Context, _ repo.Config, a model.RepositoryAssignment) error {
		out = a
		return nil
	})
	if err != nil {
		return model.RepositoryAssignment{}, err
	}
	return out, nil
}

// AttachRepository binds the existing key without renaming it. Configuration,
// canonical environments and assignment publication share the same transaction;
// a validation or persistence failure leaves both representations unchanged.
func (s Service) AttachRepository(ctx context.Context, key, projectKey string, force bool) error {
	return s.repositoryTransaction(ctx, key, func(ctx context.Context, c repo.Config, a model.RepositoryAssignment) error {
		if err := s.requireProject(ctx, projectKey); err != nil {
			return err
		}
		if c.ProjectKey != "" && c.ProjectKey != projectKey && !force {
			return fmt.Errorf("repository %q already belongs to project %q: %w", key, c.ProjectKey, ErrInvalid)
		}
		c.ProjectKey = projectKey
		if err := s.Repositories.CheckEnvironments(ctx, &c); err != nil {
			return fmt.Errorf("project: validate repository environments: %w", err)
		}
		return s.publishRepositoryBinding(ctx, c, a)
	})
}

// DetachRepository removes the owner while keeping standard shares. The
// repository environment validator clears its project environment selection;
// its content, key and unrelated configuration remain unchanged.
func (s Service) DetachRepository(ctx context.Context, key string) error {
	return s.repositoryTransaction(ctx, key, func(ctx context.Context, c repo.Config, a model.RepositoryAssignment) error {
		c.ProjectKey = ""
		if err := s.Repositories.CheckEnvironments(ctx, &c); err != nil {
			return fmt.Errorf("project: validate detached repository: %w", err)
		}
		return s.publishRepositoryBinding(ctx, c, a)
	})
}

// ShareRepository adds a target or the all-project target without changing the
// owner. Sharing has one repository-wide permission ceiling, as represented by
// the status API; readOnly never selects a producer-side sharing mode.
func (s Service) ShareRepository(ctx context.Context, key, target string, all, readOnly bool) error {
	return s.repositoryTransaction(ctx, key, func(ctx context.Context, c repo.Config, a model.RepositoryAssignment) error {
		if c.Class != repo.Local && c.Class != repo.Remote {
			return fmt.Errorf("only local and remote repositories can be shared: %w", ErrInvalid)
		}
		if all {
			a.SharedWithAll = true
		} else {
			if err := s.requireProject(ctx, target); err != nil {
				return err
			}
			if !slices.Contains(a.SharedWith, target) {
				a.SharedWith = append(a.SharedWith, target)
			}
		}
		a.SharedReadOnly = readOnly
		return s.publishRepositoryShares(ctx, a)
	})
}

// UnshareRepository removes only the selected target kind. Removing the
// all-project share keeps explicit targets, and removing an explicit target
// keeps the all-project share. Neither operation changes canonical ownership.
func (s Service) UnshareRepository(ctx context.Context, key, target string, all bool) error {
	return s.repositoryTransaction(ctx, key, func(ctx context.Context, _ repo.Config, a model.RepositoryAssignment) error {
		if all {
			a.SharedWithAll = false
		} else {
			if err := s.requireProject(ctx, target); err != nil {
				return err
			}
			a.SharedWith = slices.DeleteFunc(a.SharedWith, func(project string) bool { return project == target })
		}
		return s.publishRepositoryShares(ctx, a)
	})
}

func (s Service) publishRepositoryShares(ctx context.Context, a model.RepositoryAssignment) error {
	a.Updated = time.Now().UTC()
	if err := s.Store.Projects().PutAssignment(ctx, a); err != nil {
		return fmt.Errorf("project: publish repository shares: %w", err)
	}
	return nil
}

func (s Service) repositoryTransaction(
	ctx context.Context, key string,
	fn func(context.Context, repo.Config, model.RepositoryAssignment) error,
) error {
	if s.Repositories == nil {
		return fmt.Errorf("project: repository binding is unavailable: %w", kernel.ErrStore)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return err
		}
		c, err := s.Repositories.Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("repository %q: %w", key, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("project: read repository %q: %w", key, err)
		}
		a, err := s.Store.Projects().Assignment(ctx, c.Key)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("project: read repository assignment: %w", err)
		}
		// Repository configuration is the owner/environment authority, including
		// records created through the repository API before any explicit attach.
		a.RepoKey, a.ProjectKey = c.Key, c.ProjectKey
		a.Environments = slices.Clone(c.Environments)
		return fn(ctx, c, a)
	})
}

func (s Service) publishRepositoryBinding(ctx context.Context, c repo.Config, a model.RepositoryAssignment) error {
	if err := s.Repositories.Update(ctx, c); err != nil {
		return fmt.Errorf("project: update repository binding: %w", err)
	}
	a.ProjectKey, a.Environments = c.ProjectKey, slices.Clone(c.Environments)
	a.Updated = time.Now().UTC()
	if err := s.Store.Projects().PutAssignment(ctx, a); err != nil {
		return fmt.Errorf("project: publish repository assignment: %w", err)
	}
	return nil
}
