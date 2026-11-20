package federation

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// Store is federation's metadata plus the instance settings and proxy catalogue
// read by replication configuration. All methods share one unit of work.
type Store interface {
	port.Store
	Settings() settings.SettingsStore
	Configs() configdoc.ConfigStore
}

// Deps supplies current authority and canonical repository configuration over
// the same guarded transaction. Zone is the server's schedule timezone.
type Deps struct {
	Store        Store
	Access       accessapi.API
	Ownership    artifactsapi.ProjectRepositoryLookup
	Repositories artifactsapi.Repositories
	Zone         string
}

var errNoStore = errors.New("federation: Deps.Store is required")

// Module owns single-target configuration. Transfer and execution are separate
// services; constructing this module starts no workers and makes no remote I/O.
type Module struct{ deps Deps }

// New constructs the module without reading or writing its dependencies.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	return &Module{deps: d}, nil
}

// Configurations supplies administration and the private configuration revision
// admission contract. It does not implement a transfer or dispatch a run.
func (m *Module) Configurations() replication.Service {
	d := m.deps
	s := replication.Service{
		Store: d.Store, Settings: d.Store.Settings(), Ownership: d.Ownership,
		Repositories: d.Repositories, Authorize: d.currentPolicy, Zone: d.Zone,
		ProxyExists: func(ctx context.Context, key string) error {
			_, err := d.Store.Configs().Get(ctx, configdoc.ConfigProxy, key)
			if err != nil {
				return fmt.Errorf("replication proxy: %w", err)
			}
			return nil
		},
	}
	if d.Access.Projects != nil {
		s.Guard = d.Access.Projects.Guard
	}
	return s
}

type policy struct{ decider accessapi.Decider }

func (p policy) Any() bool      { return p.decider.HasProjectAdministration() }
func (p policy) Platform() bool { return p.decider.ManagesProject("", accessapi.ProjectManage) }
func (p policy) Allows(project string, class repo.Class) bool {
	return p.decider.ManagesRepository(project, string(class), accessapi.RepositoryUpdate)
}

func (d Deps) currentPolicy(ctx context.Context) (replication.Policy, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return nil, fmt.Errorf("replication caller: %w", err)
	}
	if d.Access.Projects == nil || d.Access.Authenticator == nil || d.Access.Authorizer == nil {
		return nil, fmt.Errorf("replication access contracts are unavailable: %w", kernel.ErrStore)
	}
	if err := d.Access.Projects.Guard(ctx); err != nil {
		return nil, fmt.Errorf("guard replication administration: %w", err)
	}
	p, err = d.Access.Authenticator.Current(ctx, p)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, kernel.ErrBadCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("current replication credential: %w", err)
	}
	decider, err := d.Access.Authorizer.For(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("current replication permissions: %w", err)
	}
	if decider == nil {
		return nil, kernel.ErrForbidden
	}
	return policy{decider: decider}, nil
}
