package retention

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// Store composes retention configuration and its durable schedule positions.
type Store interface {
	port.Store
	Schedules() schedule.Store
}

// Deps supplies current access and repository ownership over the same store.
type Deps struct {
	Store        Store
	Access       accessapi.API
	Repositories artifactsapi.ProjectRepositoryLookup
	// Zone is the server's schedule timezone; empty means UTC.
	Zone string
}

var errNoStore = errors.New("retention: Deps.Store is required")

// Module owns package policy administration. Cleanup selection, execution and
// reports keep their CLN-03/04 owners; archive/restore keep their P16 owners.
type Module struct{ deps Deps }

// New constructs the module without starting a scheduler or doing any I/O.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	return &Module{deps: d}, nil
}

// Policies supplies package configuration and the queued-generation fence.
func (m *Module) Policies() cleanup.Service {
	d := m.deps
	s := cleanup.Service{Store: d.Store, Schedules: d.Store.Schedules(), Repositories: d.Repositories, Zone: d.Zone, Authorize: d.currentPolicy}
	if d.Access.Projects != nil {
		s.Guard, s.ProjectExists = d.Access.Projects.Guard, d.Access.Projects.Exists
	}
	return s
}

type policy struct {
	decider accessapi.Decider
	name    string
}

func (p policy) Any() bool { return p.decider.HasProjectAdministration() }
func (p policy) Allows(project string) bool {
	return p.decider.ManagesProject(project, accessapi.ProjectManage)
}
func (p policy) Name() string { return p.name }

func (d Deps) currentPolicy(ctx context.Context, _ kernel.ScopeActions) (cleanup.Policy, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return nil, fmt.Errorf("cleanup caller: %w", err)
	}
	if d.Access.Projects == nil || d.Access.Authenticator == nil || d.Access.Authorizer == nil {
		return nil, fmt.Errorf("cleanup access contracts are unavailable: %w", kernel.ErrStore)
	}
	if err := d.Access.Projects.Guard(ctx); err != nil {
		return nil, fmt.Errorf("guard cleanup administration: %w", err)
	}
	p, err = d.Access.Authenticator.Current(ctx, p)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, kernel.ErrBadCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("current cleanup credential: %w", err)
	}
	decider, err := d.Access.Authorizer.For(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("current cleanup permissions: %w", err)
	}
	if decider == nil {
		return nil, kernel.ErrForbidden
	}
	return policy{decider: decider, name: p.Name}, nil
}
