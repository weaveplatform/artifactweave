package project

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Guard takes the administration snapshot without materializing projects.
// Its caller holds the enclosing transaction through its authority check and
// publication; paged discovery therefore needs no preliminary complete list.
func (s Service) Guard(ctx context.Context) error {
	if err := s.Store.Projects().Guard(ctx); err != nil {
		return fmt.Errorf("project: guard administration: %w", err)
	}
	return nil
}

// Projects returns projects in stored key order. The caller applies visibility
// and administration policy before publishing the result.
func (s Service) Projects(ctx context.Context) ([]model.Project, error) {
	all, err := s.Store.Projects().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("project: list projects: %w", err)
	}
	return all, nil
}

// Project reads one project's settings without manufacturing authorization.
func (s Service) Project(ctx context.Context, key string) (model.Project, error) {
	p, err := s.Store.Projects().Get(ctx, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.Project{}, fmt.Errorf("project %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return model.Project{}, fmt.Errorf("project: read project %q: %w", key, err)
	}
	return p, nil
}

func validProject(p model.Project) error {
	if err := model.ValidateProjectKey(p.Key); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		return fmt.Errorf("project display name is required: %w", ErrInvalid)
	}
	if p.StorageQuotaBytes < -1 {
		return fmt.Errorf("project storage quota must be -1 or nonnegative: %w", ErrInvalid)
	}
	return nil
}

func (s Service) availableDisplayName(ctx context.Context, p model.Project) error {
	all, err := s.Projects(ctx)
	if err != nil {
		return err
	}
	for _, other := range all {
		if other.Key != p.Key && other.DisplayName == p.DisplayName {
			return fmt.Errorf("project display name %q: %w", p.DisplayName, ErrConflict)
		}
	}
	return nil
}

// CreateProject creates the settings and server-owned build-info repository in
// one unit of work. Listing projects takes the global project guard before any
// artifact locks, including when the project set is initially empty.
func (s Service) CreateProject(ctx context.Context, p model.Project) error {
	if err := validProject(p); err != nil {
		return err
	}
	if s.Repositories == nil {
		return fmt.Errorf("project: repository binding is unavailable: %w", kernel.ErrStore)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.availableDisplayName(ctx, p); err != nil {
			return err
		}
		if err := s.Store.Projects().Create(ctx, p); err != nil {
			if errors.Is(err, kernel.ErrConflict) {
				return fmt.Errorf("project %q: %w", p.Key, ErrConflict)
			}
			return fmt.Errorf("project: create %q: %w", p.Key, err)
		}
		builds := repo.Config{
			Key: p.Key + "-build-info", Class: repo.Local, PackageType: repo.BuildInfo,
			ProjectKey: p.Key, Environments: []string{"DEV"},
			Description: "Build Info repository", RepoLayoutRef: repo.LayoutSimple,
		}
		if err := s.Repositories.Create(ctx, *repo.Defaults(&builds)); err != nil {
			if errors.Is(err, kernel.ErrConflict) {
				return fmt.Errorf("project build-info repository %q already exists: %w", builds.Key, ErrConflict)
			}
			return fmt.Errorf("project: create build-info repository %q: %w", builds.Key, err)
		}
		return nil
	})
}

// UpdateProject replaces mutable settings under the path's immutable key. The
// store rotates policy authority; quota admission is a separate P11 service.
func (s Service) UpdateProject(ctx context.Context, key string, p model.Project) error {
	if p.Key != "" && p.Key != key {
		return fmt.Errorf("project key cannot be changed: %w", ErrInvalid)
	}
	p.Key = key
	if err := validProject(p); err != nil {
		return err
	}
	return s.tx(ctx, func(ctx context.Context) error {
		if _, err := s.Project(ctx, key); err != nil {
			return err
		}
		if err := s.availableDisplayName(ctx, p); err != nil {
			return err
		}
		if err := s.Store.Projects().Update(ctx, p); err != nil {
			return fmt.Errorf("project: update %q: %w", key, err)
		}
		return nil
	})
}

// DeleteProject removes administration state while retaining owned repositories
// and their content. Configuration unassignment and access cleanup share the
// project guard and the same transaction, so a failure leaves ownership intact.
func (s Service) DeleteProject(ctx context.Context, key string) error {
	if s.Repositories == nil {
		return fmt.Errorf("project: repository binding is unavailable: %w", kernel.ErrStore)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		if _, err := s.Project(ctx, key); err != nil {
			return err
		}
		all, err := s.Repositories.List(ctx, artifactsapi.RepoFilter{})
		if err != nil {
			return fmt.Errorf("project: list repositories for deletion: %w", err)
		}
		for _, c := range all {
			if c.ProjectKey != key {
				continue
			}
			c.ProjectKey, c.Environments = "", nil
			if err := s.Repositories.Update(ctx, c); err != nil {
				return fmt.Errorf("project: unassign repository %q: %w", c.Key, err)
			}
		}
		if err := s.Store.Projects().Delete(ctx, key); err != nil {
			return fmt.Errorf("project: delete %q: %w", key, err)
		}
		return nil
	})
}
