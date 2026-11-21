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
	ItemRemoval  artifactsapi.ItemRemoval
	// Zone is the server's schedule timezone; empty means UTC.
	Zone string
}

var errNoStore = errors.New("retention: Deps.Store is required")

// Module owns package policy administration and execution. Report formatting
// retains CLN-05; archive/restore retain P16. Construction starts no worker.
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

// Executor binds normal artifact removal to durable cleanup state.
func (m *Module) Executor() (cleanup.Executor, error) {
	store, ok := m.deps.Store.(port.CleanupExecutionStore)
	if !ok || m.deps.ItemRemoval == nil || m.deps.PackageIndex == nil || m.deps.Access.Projects == nil || m.deps.Access.Authenticator == nil || m.deps.Access.Authorizer == nil {
		return cleanup.Executor{}, fmt.Errorf("cleanup execution contracts unavailable: %w", kernel.ErrStore)
	}
	return cleanup.Executor{Store: store, Policies: m.Policies(), Removal: m.deps.ItemRemoval}, nil
}

// RegisterWork binds the persisted run kind before the application starts its
// queue. It does not lease work or start a goroutine.
func (m *Module) RegisterWork(queue *work.Queue) error {
	if queue == nil {
		return fmt.Errorf("cleanup work queue unavailable: %w", kernel.ErrStore)
	}
	executor, err := m.Executor()
	if err != nil {
		return err
	}
	if err := queue.Handle(cleanup.WorkKind, executor.Execute); err != nil {
		return fmt.Errorf("register cleanup execution: %w", err)
	}
	return nil
}

// ScheduleHandlers supplies the shared scheduler's package-policy admission
// handler. The occurrence and run queue writes share its final lease fence.
func (m *Module) ScheduleHandlers() map[string]schedule.Handler {
	policies := m.Policies()
	return map[string]schedule.Handler{cleanup.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
		if _, err := policies.ScheduledRun(ctx, trigger); err != nil {
			return fmt.Errorf("admit scheduled package cleanup: %w", err)
		}
		return nil
	}}
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
	var authority policy
	err := d.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		authority, err = d.currentAuthority(ctx, reference.Principal())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("restore cleanup caller authority: %w", err)
	}
	// Bind the original context: a page reader must not retain the closed
	// transaction used to restore it; joined mutation callers keep theirs.
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
