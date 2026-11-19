package project

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// DefaultEnvironmentLimit is the documented default number of project-defined
// environments; global environments are not part of this project's quota.
const DefaultEnvironmentLimit = 10

// Environments lists only the environments defined by key, in stored order.
func (s Service) Environments(ctx context.Context, key string) ([]model.Environment, error) {
	var out []model.Environment
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		var err error
		out, err = s.Store.Projects().Environments(ctx, key)
		if err != nil {
			return fmt.Errorf("project: list environments: %w", err)
		}
		return nil
	})
	return out, err
}

func checkedEnvironmentName(key, name string) (string, error) {
	prefix := key + "-"
	if len(name) <= len(prefix) || !strings.EqualFold(name[:len(prefix)], prefix) || !model.ValidEnvironmentName(name) {
		return "", fmt.Errorf("environment must have the project prefix and contain at most 32 letters, digits, hyphens or underscores: %w", ErrInvalid)
	}
	return prefix + name[len(prefix):], nil
}

func (s Service) projectEnvironment(ctx context.Context, key, name string) (model.Environment, error) {
	all, err := s.Environments(ctx, key)
	if err != nil {
		return model.Environment{}, err
	}
	for _, e := range all {
		if strings.EqualFold(e.Name, name) {
			return e, nil
		}
	}
	return model.Environment{}, fmt.Errorf("environment %q: %w", name, ErrNotFound)
}

func (s Service) environmentNameAvailable(ctx context.Context, name string) error {
	projects, err := s.Projects(ctx)
	if err != nil {
		return err
	}
	keys := []string{""}
	for _, p := range projects {
		keys = append(keys, p.Key)
	}
	for _, key := range keys {
		all, err := s.Store.Projects().Environments(ctx, key)
		if err != nil {
			return fmt.Errorf("project: check environment name: %w", err)
		}
		for _, e := range all {
			if strings.EqualFold(e.Name, name) {
				return fmt.Errorf("environment %q: %w", name, ErrConflict)
			}
		}
	}
	return nil
}

// CreateEnvironment stores the fully prefixed name sent by the native project
// provider. The prefix participates in the documented 32-character name bound.
func (s Service) CreateEnvironment(ctx context.Context, key, name string) (model.Environment, error) {
	name, err := checkedEnvironmentName(key, name)
	if err != nil {
		return model.Environment{}, err
	}
	out := model.Environment{ProjectKey: key, Name: name}
	err = s.tx(ctx, func(ctx context.Context) error {
		all, err := s.Environments(ctx, key)
		if err != nil {
			return err
		}
		if len(all) >= DefaultEnvironmentLimit {
			return fmt.Errorf("at most %d project environments may exist: %w", DefaultEnvironmentLimit, ErrInvalid)
		}
		if err := s.environmentNameAvailable(ctx, name); err != nil {
			return err
		}
		if err := s.Store.Projects().CreateEnvironment(ctx, out); err != nil {
			return fmt.Errorf("project: create environment: %w", err)
		}
		return nil
	})
	return out, err
}

// RenameEnvironment changes the stored spelling and every role, assignment and
// repository reference atomically. A name differing only in case is the same
// environment, not a conflict with itself.
func (s Service) RenameEnvironment(ctx context.Context, key, name, to string) (model.Environment, error) {
	to, err := checkedEnvironmentName(key, to)
	if err != nil {
		return model.Environment{}, err
	}
	if s.Repositories == nil {
		return model.Environment{}, fmt.Errorf("project: repository binding is unavailable: %w", kernel.ErrStore)
	}
	out := model.Environment{ProjectKey: key, Name: to}
	err = s.tx(ctx, func(ctx context.Context) error {
		from, err := s.projectEnvironment(ctx, key, name)
		if err != nil {
			return err
		}
		if !strings.EqualFold(from.Name, to) {
			if err := s.environmentNameAvailable(ctx, to); err != nil {
				return err
			}
		}
		if err := s.Store.Projects().RenameEnvironment(ctx, key, from.Name, to); err != nil {
			return fmt.Errorf("project: rename environment: %w", err)
		}
		all, err := s.Repositories.List(ctx, artifactsapi.RepoFilter{})
		if err != nil {
			return fmt.Errorf("project: list repositories for environment rename: %w", err)
		}
		for _, c := range all {
			changed := false
			c.Environments = slices.Clone(c.Environments)
			for i, env := range c.Environments {
				if strings.EqualFold(env, from.Name) {
					c.Environments[i], changed = to, true
				}
			}
			if changed {
				if err := s.Repositories.Update(ctx, c); err != nil {
					return fmt.Errorf("project: rename environment in repository %q: %w", c.Key, err)
				}
			}
		}
		return nil
	})
	return out, err
}

// DeleteEnvironment refuses a still-referenced project environment. Global
// built-ins are not project-owned records and cannot be removed here.
func (s Service) DeleteEnvironment(ctx context.Context, key, name string) error {
	if s.Repositories == nil {
		return fmt.Errorf("project: repository binding is unavailable: %w", kernel.ErrStore)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		e, err := s.projectEnvironment(ctx, key, name)
		if err != nil {
			return err
		}
		all, err := s.Repositories.List(ctx, artifactsapi.RepoFilter{})
		if err != nil {
			return fmt.Errorf("project: check environment repositories: %w", err)
		}
		for _, c := range all {
			if slices.ContainsFunc(c.Environments, func(n string) bool { return strings.EqualFold(n, e.Name) }) {
				return fmt.Errorf("environment %q is assigned to a repository: %w", e.Name, ErrInvalid)
			}
		}
		roles, err := s.Store.Projects().Roles(ctx, key)
		if err != nil {
			return fmt.Errorf("project: check environment roles: %w", err)
		}
		for _, r := range roles {
			if slices.ContainsFunc(r.Environments, func(n string) bool { return strings.EqualFold(n, e.Name) }) {
				return fmt.Errorf("environment %q is assigned to a role: %w", e.Name, ErrInvalid)
			}
		}
		assignments, err := s.Store.Projects().Assignments(ctx, key)
		if err != nil {
			return fmt.Errorf("project: check environment assignments: %w", err)
		}
		for _, assignment := range assignments {
			if slices.ContainsFunc(assignment.Environments, func(n string) bool { return strings.EqualFold(n, e.Name) }) {
				return fmt.Errorf("environment %q is assigned to a repository: %w", e.Name, ErrInvalid)
			}
		}
		if err := s.Store.Projects().DeleteEnvironment(ctx, key, e.Name); err != nil {
			return fmt.Errorf("project: delete environment: %w", err)
		}
		return nil
	})
}
