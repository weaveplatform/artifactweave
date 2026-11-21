package replication

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// ErrMultipleTargets keeps pre-existing multi-push configurations with their
// advanced-replication owner instead of silently reading or replacing one row.
var ErrMultipleTargets = errors.New("replication: multiple targets are not supported")

// ErrAdvancedConfiguration retains legacy advanced options with their owner
// instead of publishing them as an executing single-target configuration.
var ErrAdvancedConfiguration = errors.New("replication: advanced configuration is not supported")

// Policy is current configuration authority, refreshed under the project guard.
// Any gates repository existence checks; Allows applies the current owner/class
// privilege. Platform is required for instance-wide configuration controls.
type Policy interface {
	Any() bool
	Allows(projectKey string, class repo.Class) bool
	Platform() bool
}

// Input distinguishes omitted values from explicit false/empty values. Replace
// applies public defaults; Update preserves omitted fields. Both retain an
// omitted password, because the underlying store Put replaces a complete model.
type Input struct {
	URL, Username, Password                              *string
	Enabled, SyncDeletes, SyncProperties, SyncStatistics *bool
	CronExp, IncludePathPrefix, ExcludePathPrefix, Proxy *string
	DisableProxy, EventReplication, CheckBinaryExistence *bool
	SocketTimeoutMillis                                  *int64
}

// View contains only public configuration, including a masked password. It
// deliberately has no private revision and no reference to a stored model.
type View struct {
	RepoKey, Key, URL, Username, Password       string
	Enabled                                     bool
	CronExp                                     string
	SyncDeletes, SyncProperties, SyncStatistics bool
	IncludePathPrefix, ExcludePathPrefix, Proxy string
	DisableProxy                                bool
	SocketTimeout                               time.Duration
}

func view(r model.Replication) View {
	return View{
		RepoKey: r.RepoKey, Key: r.Key, URL: r.URL, Username: r.Username, Password: crypt.Mask(r.Password),
		Enabled: r.Enabled, CronExp: r.CronExp, SyncDeletes: r.SyncDeletes,
		SyncProperties: r.SyncProperties, SyncStatistics: r.SyncStatistics,
		IncludePathPrefix: r.IncludePathPrefix, ExcludePathPrefix: r.ExcludePathPrefix,
		Proxy: r.Proxy, DisableProxy: r.DisableProxy, SocketTimeout: r.SocketTimeout,
	}
}

// Service owns single-target configuration. Authorize acquires the shared
// project guard and refreshes identity and permissions inside Store's unit of
// work. Repository configuration and proxy reads happen only after authority
// checks. Guard supplies the same ordering to private worker admission.
type Service struct {
	Store        port.ConfigurationStore
	Ownership    artifactsapi.ProjectRepositoryLookup
	Repositories interface {
		Get(context.Context, string) (repo.Config, error)
	}
	Settings    settings.SettingsStore
	ProxyExists func(context.Context, string) error
	Authorize   func(context.Context) (Policy, error)
	Guard       func(context.Context) error
	Zone        string
	Schedules   schedule.Store
	// ScheduleAuthor supplies a verified reference in tests/composition. Nil
	// uses the authenticated request principal already checked by Authorize.
	ScheduleAuthor func(context.Context) (kernel.CredentialReference, error)
	Now            func() time.Time
}

func (s Service) run(ctx context.Context, fn func(context.Context, Policy) error) error {
	if s.Store == nil {
		return fmt.Errorf("replication store is unavailable: %w", kernel.ErrStore)
	}
	if s.Authorize == nil {
		return kernel.ErrForbidden
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		policy, err := s.Authorize(ctx)
		if err != nil {
			return fmt.Errorf("current replication authority: %w", err)
		}
		if policy == nil || !policy.Any() {
			return kernel.ErrForbidden
		}
		return fn(ctx, policy)
	})
	if err != nil {
		return fmt.Errorf("replication administration: %w", err)
	}
	return nil
}

func (s Service) repository(ctx context.Context, key string, policy Policy) (artifactsapi.ProjectRepository, error) {
	if s.Ownership == nil {
		return artifactsapi.ProjectRepository{}, fmt.Errorf("replication repository lookup is unavailable: %w", kernel.ErrStore)
	}
	r, err := s.Ownership.ProjectRepository(ctx, key)
	if err != nil {
		return r, fmt.Errorf("read replication repository: %w", err)
	}
	if !policy.Allows(r.ProjectKey, r.Class) {
		return artifactsapi.ProjectRepository{}, kernel.ErrForbidden
	}
	if _, ok := model.DirectionFor(r.Class); !ok {
		return artifactsapi.ProjectRepository{}, fmt.Errorf("only local and remote repositories support single replication: %w", kernel.ErrInvalid)
	}
	return r, nil
}

func (s Service) target(ctx context.Context, key string) (model.Replication, error) {
	rows, err := s.Store.Replications().ForRepo(ctx, key)
	if err != nil {
		return model.Replication{}, fmt.Errorf("read replication configuration: %w", err)
	}
	if len(rows) > 1 {
		return model.Replication{}, ErrMultipleTargets
	}
	if len(rows) == 0 {
		return model.Replication{}, kernel.ErrNotFound
	}
	return rows[0], nil
}

// Get publishes a redacted configuration only after the guarded read commits.
func (s Service) Get(ctx context.Context, key string) (View, error) {
	var out View
	err := s.run(ctx, func(ctx context.Context, policy Policy) error {
		if _, err := s.repository(ctx, key, policy); err != nil {
			return err
		}
		r, err := s.target(ctx, key)
		if err != nil {
			return err
		}
		if r.EventReplication || r.CheckBinaryExistence {
			return ErrAdvancedConfiguration
		}
		out = view(r)
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return out, nil
}

// Replace stores one whole configuration with native defaults. Existing target
// identity and an omitted password survive replacement; all other omitted
// public fields reset. A new configuration always gets a fresh public key.
func (s Service) Replace(ctx context.Context, key string, in Input) error {
	return s.save(ctx, key, in, true)
}

// Update changes only supplied fields of an existing single configuration.
func (s Service) Update(ctx context.Context, key string, in Input) error {
	return s.save(ctx, key, in, false)
}

func (s Service) save(ctx context.Context, key string, in Input, replace bool) error {
	return s.run(ctx, func(ctx context.Context, policy Policy) error {
		repository, err := s.repository(ctx, key, policy)
		if err != nil {
			return err
		}
		old, err := s.target(ctx, key)
		fresh := errors.Is(err, kernel.ErrNotFound)
		if err != nil && (!fresh || !replace) {
			return err
		}
		next := old
		if replace {
			next = model.Replication{RepoKey: key, Key: old.Key, Password: old.Password, Enabled: true, SyncProperties: true, SocketTimeout: 15 * time.Second}
		}
		if fresh {
			next.Key = rand.Text()
		}
		if err := s.merge(ctx, repository, old, &next, in); err != nil {
			return err
		}
		if err := s.bindScheduleAuthor(ctx, &next); err != nil {
			return err
		}
		next.Revision = old.Revision
		if old.Revision != "" && next == old {
			return nil
		}
		next.Revision = rand.Text()
		if err := s.Store.Replications().Put(ctx, next); err != nil {
			return fmt.Errorf("write replication configuration: %w", err)
		}
		return s.publishSchedule(ctx, next)
	})
}

// Delete removes the target and owned run history atomically. A URL filter
// selects the exact target; a different URL must not delete another target.
func (s Service) Delete(ctx context.Context, key, targetURL string) error {
	return s.run(ctx, func(ctx context.Context, policy Policy) error {
		if _, err := s.repository(ctx, key, policy); err != nil {
			return err
		}
		r, err := s.target(ctx, key)
		if err != nil {
			return err
		}
		if targetURL != "" && r.URL != targetURL {
			return kernel.ErrNotFound
		}
		if err := s.Store.Replications().Delete(ctx, r.Key); err != nil {
			return fmt.Errorf("delete replication configuration: %w", err)
		}
		return s.removeSchedule(ctx, r.Key)
	})
}

// WithTarget admits bounded worker bookkeeping at the current private revision.
// Callers separately authorize execution; this is only configuration admission.
// fn joins the guarded transaction and must not transfer content or make remote
// calls. REP-04 owns the worker lease and network-effect reconciliation.
func (s Service) WithTarget(ctx context.Context, key, revision string, fn func(context.Context, model.Replication) error) error {
	if s.Store == nil || s.Guard == nil || s.Ownership == nil {
		return fmt.Errorf("replication worker contracts are unavailable: %w", kernel.ErrStore)
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return fmt.Errorf("guard replication configuration: %w", err)
		}
		r, err := s.Store.Replications().Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read replication worker target: %w", err)
		}
		if revision == "" || r.Revision != revision || !r.Enabled {
			return kernel.ErrConflict
		}
		current, err := s.target(ctx, r.RepoKey)
		if errors.Is(err, ErrMultipleTargets) {
			return kernel.ErrConflict
		}
		if err != nil {
			return err
		}
		if current.Key != r.Key || current.Revision != r.Revision {
			return kernel.ErrConflict
		}
		repository, err := s.Ownership.ProjectRepository(ctx, r.RepoKey)
		if err != nil {
			return fmt.Errorf("read replication worker repository: %w", err)
		}
		direction, supported := model.DirectionFor(repository.Class)
		if !supported || r.EventReplication || r.CheckBinaryExistence {
			return kernel.ErrConflict
		}
		blocks, err := s.blocks(ctx)
		if err != nil {
			return err
		}
		if direction == model.ReplicationPush && blocks.Push || direction == model.ReplicationPull && blocks.Pull {
			return kernel.ErrConflict
		}
		return fn(ctx, r)
	})
	if err != nil {
		return fmt.Errorf("replication worker admission: %w", err)
	}
	return nil
}
