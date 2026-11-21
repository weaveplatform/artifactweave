package accessapitest

import (
	"context"
	"fmt"
	"slices"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ accessapi.Projects = (*Projects)(nil)

// Project is one project as Projects holds it.
type Project struct {
	DisplayName string
	// Incarnation is the explicit lifetime used by durable-work tests.
	Incarnation string
	// Environments are the project's environment names, in any order;
	// Projects.Environments sorts them.
	Environments []string
	// Members are the names of the users who are members of the project.
	Members []string
}

// Projects is a Projects over a map of projects and a list of global
// environments, which it only reads. A project missing from the map exists
// for no call: it has no environments and no members, and Exists answers an
// error matching kernel.ErrNotFound. Err, when set, answers every call
// instead, as a store that cannot be read does. It may be used from several
// goroutines while no one changes its fields.
type Projects struct {
	// GuardFunc can observe or refuse transactional authority admission.
	GuardFunc            func(context.Context) error
	BindRepositoryFunc   func(context.Context, string, string, []string, bool) error
	RemoveRepositoryFunc func(context.Context, string) error
	IncarnationFunc      func(context.Context, string) (string, error)
	// Projects maps a project key to the project.
	Projects map[string]Project
	// Global is the global environments' names, in any order, which
	// Environments returns for project "".
	Global []string
	// Err, when set, is every call's error.
	Err error
}

// BindRepository observes binding publication when configured, or returns Err.
func (p *Projects) BindRepository(ctx context.Context, key, project string, environments []string, fresh bool) error {
	if p.BindRepositoryFunc != nil {
		return p.BindRepositoryFunc(ctx, key, project, environments, fresh)
	}
	return p.Err
}

// RemoveRepository observes binding removal when configured, or returns Err.
func (p *Projects) RemoveRepository(ctx context.Context, key string) error {
	if p.RemoveRepositoryFunc != nil {
		return p.RemoveRepositoryFunc(ctx, key)
	}
	return p.Err
}

// Guard runs GuardFunc, when supplied, or returns Err.
func (p *Projects) Guard(ctx context.Context) error {
	if p.GuardFunc != nil {
		return p.GuardFunc(ctx)
	}
	return p.Err
}

// Environments returns the names of project's environments, or of the
// global ones for project "", sorted by name in a new slice; none for a
// project that does not exist. Err, when set, is returned instead.
func (p *Projects) Environments(_ context.Context, project string) ([]string, error) {
	if p.Err != nil {
		return nil, p.Err
	}
	envs := p.Global
	if project != "" {
		envs = p.Projects[project].Environments
	}
	names := append([]string{}, envs...)
	slices.Sort(names)
	return names, nil
}

// Member returns nil when user is among project's Members, Err, or an error
// matching kernel.ErrNotFound when the user is not a member or the project
// does not exist.
func (p *Projects) Member(_ context.Context, project, user string) error {
	if p.Err != nil {
		return p.Err
	}
	if pr, ok := p.Projects[project]; ok && slices.Contains(pr.Members, user) {
		return nil
	}
	return fmt.Errorf("accessapitest: project %q member %q: %w", project, user, kernel.ErrNotFound)
}

// Exists returns nil when Projects holds project, Err, or an error matching
// kernel.ErrNotFound when it does not.
func (p *Projects) Exists(_ context.Context, project string) error {
	if p.Err != nil {
		return p.Err
	}
	if _, ok := p.Projects[project]; ok {
		return nil
	}
	return fmt.Errorf("accessapitest: project %q: %w", project, kernel.ErrNotFound)
}

// DisplayName returns only the stored descriptive label, or the read failure.
func (p *Projects) DisplayName(_ context.Context, project string) (string, error) {
	if p.Err != nil {
		return "", p.Err
	}
	if value, ok := p.Projects[project]; ok {
		return value.DisplayName, nil
	}
	return "", fmt.Errorf("accessapitest: project %q: %w", project, kernel.ErrNotFound)
}

// Incarnation returns the configured lifetime or a store failure for an
// existing fixture that has no explicit lifetime; a key alone is no fence.
func (p *Projects) Incarnation(ctx context.Context, project string) (string, error) {
	if p.IncarnationFunc != nil {
		return p.IncarnationFunc(ctx, project)
	}
	if p.Err != nil {
		return "", p.Err
	}
	value, ok := p.Projects[project]
	if !ok {
		return "", fmt.Errorf("accessapitest: project %q: %w", project, kernel.ErrNotFound)
	}
	if value.Incarnation == "" {
		return "", fmt.Errorf("accessapitest: project has no explicit lifetime: %w", kernel.ErrStore)
	}
	return value.Incarnation, nil
}
