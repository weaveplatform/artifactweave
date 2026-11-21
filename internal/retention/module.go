package retention

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// Store composes retention configuration and its durable schedule positions.
type Store interface {
	port.Store
	Schedules() schedule.Store
	WorkQueue() work.WorkQueue
}

// Deps supplies current access and repository ownership over the same store.
type Deps struct {
	Store        Store
	Access       accessapi.API
	Repositories artifactsapi.ProjectRepositoryLookup
	PackageIndex artifactsapi.PackageIndex
	// Zone is the server's schedule timezone; empty means UTC.
	Zone string
}

var errNoStore = errors.New("retention: Deps.Store is required")

// Module owns package policy administration, selection and run admission.
// The CLN-04 executor and CLN-05 report adapter remain separate; archive/restore
// retain P16. Construction starts no worker or scheduler.
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
	s.Restore = d.restorePolicyCaller
	s.Work = d.Store.WorkQueue()
	s.PackageIndex = d.PackageIndex
	if d.Access.Projects != nil {
		s.Guard, s.ProjectExists = d.Access.Projects.Guard, d.Access.Projects.Exists
		s.ProjectIncarnation = d.Access.Projects.Incarnation
	}
	return s
}

type policy struct {
	principal  kernel.Principal
	decider    accessapi.Decider
	name       string
	credential kernel.CredentialReference
}

func (p policy) Any() bool { return p.decider.HasProjectAdministration() }
func (p policy) Allows(project string) bool {
	return p.decider.ManagesProject(project, accessapi.ProjectManage)
}
func (p policy) Name() string                           { return p.name }
func (p policy) Credential() kernel.CredentialReference { return kernel.Clone(p.credential) }

func (d Deps) currentPolicy(ctx context.Context) (cleanup.Policy, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return nil, fmt.Errorf("cleanup caller: %w", err)
	}
	authority, err := d.currentAuthority(ctx, p)
	if err != nil {
		return nil, err
	}
	return authority, nil
}

func (d Deps) restorePolicyCaller(ctx context.Context, reference kernel.CredentialReference) (context.Context, error) {
	if err := model.ValidateCleanupCredential(reference); err != nil {
		return nil, fmt.Errorf("cleanup caller reference: %w", err)
	}
	authority, err := d.currentAuthority(ctx, reference.Principal())
	if err != nil {
		return nil, err
	}
	return accessapi.WithCaller(ctx, authority.principal, authority.decider), nil
}

func (d Deps) currentAuthority(ctx context.Context, p kernel.Principal) (policy, error) {
	if d.Access.Projects == nil || d.Access.Authenticator == nil || d.Access.Authorizer == nil {
		return policy{}, fmt.Errorf("cleanup access contracts are unavailable: %w", kernel.ErrStore)
	}
	if err := d.Access.Projects.Guard(ctx); err != nil {
		return policy{}, fmt.Errorf("guard cleanup administration: %w", err)
	}
	p, err := d.Access.Authenticator.Current(ctx, p)
	if errors.Is(err, kernel.ErrNotFound) {
		return policy{}, kernel.ErrBadCredentials
	}
	if err != nil {
		return policy{}, fmt.Errorf("current cleanup credential: %w", err)
	}
	decider, err := d.Access.Authorizer.For(ctx, p)
	if err != nil {
		return policy{}, fmt.Errorf("current cleanup permissions: %w", err)
	}
	if decider == nil {
		return policy{}, kernel.ErrForbidden
	}
	return policy{principal: p, decider: decider, name: p.Name, credential: p.CredentialReference()}, nil
}
