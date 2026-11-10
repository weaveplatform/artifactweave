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
	// Projects maps a project key to the project.
	Projects map[string]Project
	// Global is the global environments' names, in any order, which
	// Environments returns for project "".
	Global []string
	// Err, when set, is every call's error.
	Err error
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
