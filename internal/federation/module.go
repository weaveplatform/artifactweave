package federation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/adapters/pushhttp"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// Store is federation's metadata plus the instance settings and proxy catalogue
// read by replication configuration. All methods share one unit of work.
type Store interface {
	port.ConfigurationStore
	Settings() settings.SettingsStore
	Configs() configdoc.ConfigStore
	Schedules() schedule.Store
}

// Deps supplies current authority and canonical repository configuration over
// the same guarded transaction. Zone is the server's schedule timezone.
type Deps struct {
	Store          Store
	Access         accessapi.API
	Ownership      artifactsapi.ProjectRepositoryLookup
	Lifetimes      artifactsapi.RepositoryLifetimes
	Repositories   artifactsapi.Repositories
	Zone           string
	UserQueryLimit int
	// BaseURL is the installation's configured base URL, which stands in
	// for the descriptor's urlBase as this instance's own federated member
	// base when neither urlBase nor federatedRepoUrlBase is set.
	BaseURL string

	// streams is the stream delivery engine once OpenStreams has built it.
	// Deps is copied into every service the module hands out, which all
	// share this one pointer, so membership built before OpenStreams still
	// wakes the engine built after it.
	streams *streamRef
}

// streamRef holds the module's stream delivery engine.
type streamRef struct {
	mu sync.Mutex
	e  *streamsync.Engine
}

func (r *streamRef) get() *streamsync.Engine {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.e
}

var errNoStore = errors.New("federation: Deps.Store is required")

// Module owns single-target configuration. Transfer and execution are separate
// services; constructing this module starts no workers and makes no remote I/O.
type Module struct {
	deps            Deps
	pushMu          sync.Mutex
	push            *replication.Push
	pull            *replication.Pull
	pushConnections *pushhttp.Factory
	// repoStream is the repository stream kind OpenStreams registered,
	// whose tombstones RegisterWork prunes; deps.streams.mu guards it.
	repoStream *federated.Stream
	// configRemote and configLog are configSync's transport and log
	// (OpenConfigSync); pushMu guards them.
	configRemote federated.Remote
	configLog    *slog.Logger
	// probeRemote and probeInterval are the peer status reads' transport
	// and interval (OpenLiveness); pushMu guards them.
	probeRemote   pairing.StatusRemote
	probeInterval time.Duration
	closed        bool
}

// New constructs the module without reading or writing its dependencies.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	d.streams = &streamRef{}
	return &Module{deps: d}, nil
}

// Configurations supplies administration and the private configuration revision
// admission contract. It does not implement a transfer or dispatch a run.
func (m *Module) Configurations() replication.Service {
	return m.deps.configurations()
}

func (d Deps) configurations() replication.Service {
	s := replication.Service{
		Store: d.Store, Settings: d.Store.Settings(), Ownership: d.Ownership,
		Repositories: d.Repositories, Authorize: d.currentPolicy, Zone: d.Zone,
		Schedules:      d.Store.Schedules(),
		UserQueryLimit: d.UserQueryLimit,
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

// RepositoryLifecycle resets targets/runs at repository lifetime boundaries and
// refreshes a pull source after a canonical update. Artifacts invokes it before
// content-event stream locks, under its shared transaction and project guard.
func (m *Module) RepositoryLifecycle() artifactsapi.RepositoryLifecycle {
	return repositoryLifecycle{deps: m.deps}
}

type repositoryLifecycle struct{ deps Deps }

// RefreshRepository refreshes replication and then a federated repository's
// members, queueing configSync when they changed; a repository no longer
// federated loses its members.
func (l repositoryLifecycle) RefreshRepository(ctx context.Context, key string) error {
	if err := l.deps.configurations().RefreshRepository(ctx, key); err != nil {
		return err //nolint:wrapcheck // the service supplies repository refresh context
	}
	return l.reconcile(ctx, key)
}

// ResetRepository resets replication and drops the key's federated members,
// on a fresh create (CreatedRepository then stores the new ones) and before
// a delete.
func (l repositoryLifecycle) ResetRepository(ctx context.Context, key string) error {
	if l.deps.Access.Projects == nil {
		return fmt.Errorf("replication repository guard is unavailable: %w", kernel.ErrStore)
	}
	if err := l.deps.configurations().ResetRepository(ctx, key); err != nil {
		return err //nolint:wrapcheck // service supplies lifetime context
	}
	if ms, err := l.deps.membership(); err == nil {
		return ms.Clear(ctx, key) //nolint:wrapcheck // Clear names what failed
	}
	return nil
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
