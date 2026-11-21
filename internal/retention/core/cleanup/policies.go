package cleanup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// ScheduleKind identifies package-policy occurrences on the shared scheduler.
const ScheduleKind = "cleanup:package"

// Policy is current administration authority, refreshed under the project
// guard before any policy or repository read. Global collection access requires
// Allows(""); project administrators must supply their own project explicitly.
type Policy interface {
	Any() bool
	Allows(projectKey string) bool
	Name() string
	Credential() kernel.CredentialReference
}

// RepositoryReader reads ownership without opening remote credentials.
type RepositoryReader interface {
	ProjectRepository(context.Context, string) (artifactsapi.ProjectRepository, error)
}

// Service owns package-policy configuration and its durable execution fence.
// Authorize acquires the shared project guard and refreshes current identity
// and authority inside Store's transaction, before policy/schedule locks.
// Guard acquires that same guard for a worker's revision admission. Neither
// callback performs network I/O; run admission through WithPolicy also remains
// inside the transaction. Selection is metadata-only; CLN-04 owns execution.
type Service struct {
	Store         port.Store
	Schedules     schedule.Store
	Repositories  RepositoryReader
	PackageIndex  artifactsapi.PackageIndex
	ProjectExists func(context.Context, string) error
	Authorize     func(context.Context) (Policy, error)
	Guard         func(context.Context) error
	Work          work.WorkQueue
	Now           func() time.Time
	// Zone is the server's configured IANA timezone; empty uses deterministic UTC.
	Zone string
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (s Service) run(ctx context.Context, fn func(context.Context, Policy) error) error {
	if s.Store == nil {
		return fmt.Errorf("cleanup store is unavailable: %w", kernel.ErrStore)
	}
	if s.Authorize == nil {
		return kernel.ErrForbidden
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		policy, err := s.Authorize(ctx)
		if err != nil {
			return fmt.Errorf("current cleanup authority: %w", err)
		}
		if policy == nil || !policy.Any() {
			return kernel.ErrForbidden
		}
		return fn(ctx, policy)
	})
	if err != nil {
		return fmt.Errorf("cleanup administration: %w", err)
	}
	return nil
}

func (s Service) policy(ctx context.Context, key string, authority Policy) (model.CleanupPolicy, error) {
	p, err := s.Store.Cleanup().Policy(ctx, key)
	if err != nil {
		return model.CleanupPolicy{}, fmt.Errorf("read cleanup policy: %w", err)
	}
	if !authority.Allows(p.ProjectKey) {
		return model.CleanupPolicy{}, kernel.ErrForbidden
	}
	if p.ItemType != model.CleanupPackage {
		return model.CleanupPolicy{}, fmt.Errorf("package cleanup policy %q: %w", key, kernel.ErrNotFound)
	}
	return p, nil
}

// Create always stores an inactive package policy; enablement is a separate
// operation. The schedule and private generation commit with the policy.
func (s Service) Create(ctx context.Context, key string, in model.CleanupPolicy) (model.CleanupPolicy, error) {
	var out model.CleanupPolicy
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(in.ProjectKey) {
			return kernel.ErrForbidden
		}
		if in.Enabled {
			return fmt.Errorf("a policy must be created disabled: %w", kernel.ErrInvalid)
		}
		if err := s.validate(ctx, key, &in); err != nil {
			return err
		}
		in.Revision, in.Created, in.Updated = rand.Text(), s.now(), s.now()
		in.ExecutionCredential = kernel.CredentialReference{}
		in.CreatedBy, in.UpdatedBy = authority.Name(), authority.Name()
		if err := s.Store.Cleanup().CreatePolicy(ctx, in); err != nil {
			return fmt.Errorf("create cleanup policy: %w", err)
		}
		if err := s.publishSchedule(ctx, in); err != nil {
			return err
		}
		out = in
		return nil
	})
	if err != nil {
		return model.CleanupPolicy{}, err
	}
	return out, nil
}

// Update replaces configuration while keeping the stored owner, creation
// audit and enablement. Even a criteria-only edit fences old queued work.
func (s Service) Update(ctx context.Context, key string, in model.CleanupPolicy) (model.CleanupPolicy, error) {
	var out model.CleanupPolicy
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		old, err := s.policy(ctx, key, authority)
		if err != nil {
			return err
		}
		if in.ProjectKey != old.ProjectKey {
			return fmt.Errorf("policy project is immutable: %w", kernel.ErrInvalid)
		}
		if err := s.validate(ctx, key, &in); err != nil {
			return err
		}
		in.Created, in.CreatedBy, in.Enabled = old.Created, old.CreatedBy, old.Enabled
		if old.Revision != "" && reflect.DeepEqual(configuration(in), configuration(old)) &&
			(!old.Enabled || reflect.DeepEqual(old.ExecutionCredential, authority.Credential())) {
			out = old
			return nil
		}
		if err := s.replace(ctx, &in, authority); err != nil {
			return err
		}
		out = in
		return nil
	})
	if err != nil {
		return model.CleanupPolicy{}, err
	}
	return out, nil
}

// Get returns an authorized policy. Availability checking projects current
// literal repository keys without modifying stored criteria or their patterns.
func (s Service) Get(ctx context.Context, key string, checkAvailability bool) (model.CleanupPolicy, error) {
	var out model.CleanupPolicy
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		var err error
		out, err = s.policy(ctx, key, authority)
		if err != nil || !checkAvailability {
			return err
		}
		out.Criteria.Repos, err = s.available(ctx, out.ProjectKey, out.Criteria.Repos)
		if err != nil {
			return err
		}
		out.Criteria.ExcludedRepos, err = s.available(ctx, out.ProjectKey, out.Criteria.ExcludedRepos)
		return err
	})
	if err != nil {
		return model.CleanupPolicy{}, err
	}
	return out, nil
}

// List returns global policies when project is empty, or only that project's
// package policies. An empty store filter must not turn global into all projects.
func (s Service) List(ctx context.Context, project string) ([]model.CleanupPolicy, error) {
	out := []model.CleanupPolicy{}
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(project) {
			return kernel.ErrForbidden
		}
		all, err := s.Store.Cleanup().Policies(ctx, port.PolicyFilter{ItemType: model.CleanupPackage, ProjectKey: project})
		if err != nil {
			return fmt.Errorf("list cleanup policies: %w", err)
		}
		for _, p := range all {
			if p.ProjectKey == project {
				out = append(out, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Enable changes scheduled and manual-run eligibility without stopping a run
// already admitted. The new generation makes earlier queue entries stale.
func (s Service) Enable(ctx context.Context, key string, enabled bool) error {
	return s.run(ctx, func(ctx context.Context, authority Policy) error {
		p, err := s.policy(ctx, key, authority)
		if err != nil {
			return err
		}
		if p.Enabled == enabled && p.Revision != "" && (!enabled || reflect.DeepEqual(p.ExecutionCredential, authority.Credential())) {
			return nil
		}
		p.Enabled = enabled
		return s.replace(ctx, &p, authority)
	})
}

// DisableAll is the emergency switch for the requested global/project scope,
// including stored future item types. It retains history and requests a stop for
// admitted package runs in that scope, including a run whose policy was deleted.
func (s Service) DisableAll(ctx context.Context, project string) error {
	return s.run(ctx, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(project) {
			return kernel.ErrForbidden
		}
		all, err := s.Store.Cleanup().Policies(ctx, port.PolicyFilter{ProjectKey: project})
		if err != nil {
			return fmt.Errorf("list policies for force disable: %w", err)
		}
		for _, p := range all {
			if p.ProjectKey != project || !p.Enabled && p.Revision != "" {
				continue
			}
			p.Enabled = false
			if err := s.replace(ctx, &p, authority); err != nil {
				return err
			}
		}
		return s.stopRuns(ctx, &project)
	})
}

// Delete removes policy configuration and scheduling, keeping existing runs.
// Removing the policy also makes its queued generations ineligible for admission.
func (s Service) Delete(ctx context.Context, key string) error {
	return s.run(ctx, func(ctx context.Context, authority Policy) error {
		p, err := s.policy(ctx, key, authority)
		if err != nil {
			return err
		}
		if err := s.Store.Cleanup().DeletePolicy(ctx, key); err != nil {
			return fmt.Errorf("delete cleanup policy: %w", err)
		}
		p.Enabled, p.CronExp = false, ""
		return s.publishSchedule(ctx, p)
	})
}

func configuration(p model.CleanupPolicy) model.CleanupPolicy {
	c := p.Criteria
	// SQL JSON omits empty optional collections; the in-memory adapter retains
	// them. Compare their stored meaning so an identical PUT cannot invalidate
	// queued work merely because a backend round-trip changed [] into nil.
	for _, list := range []*[]string{&c.ExcludedRepos, &c.ExcludedPackages, &c.IncludedProjects, &c.IncludedStages, &c.IncludedPackageVersions, &c.ExcludedPackageVersions} {
		if len(*list) == 0 {
			*list = nil
		}
	}
	if len(c.IncludedProperties) == 0 {
		c.IncludedProperties = nil
	}
	if len(c.ExcludedProperties) == 0 {
		c.ExcludedProperties = nil
	}
	allStages := c.IncludeAllStages == nil || *c.IncludeAllStages
	c.IncludeAllStages = &allStages
	return model.CleanupPolicy{
		Key: p.Key, Description: p.Description, CronExp: p.CronExp, ProjectKey: p.ProjectKey,
		ItemType: p.ItemType, DurationMinutes: p.DurationMinutes, Enabled: p.Enabled, SkipTrashcan: p.SkipTrashcan, Criteria: c,
	}
}

func (s Service) replace(ctx context.Context, p *model.CleanupPolicy, authority Policy) error {
	p.ExecutionCredential = kernel.CredentialReference{}
	if p.Enabled {
		p.ExecutionCredential = authority.Credential()
		if err := model.ValidateCleanupCredential(p.ExecutionCredential); err != nil {
			return err //nolint:wrapcheck // the model identifies the missing verified caller
		}
	}
	p.Revision, p.Updated, p.UpdatedBy = rand.Text(), s.now(), authority.Name()
	if err := s.Store.Cleanup().UpdatePolicy(ctx, *p); err != nil {
		return fmt.Errorf("replace cleanup policy: %w", err)
	}
	return s.publishSchedule(ctx, *p)
}

func (s Service) publishSchedule(ctx context.Context, p model.CleanupPolicy) error {
	if s.Schedules == nil {
		return fmt.Errorf("cleanup scheduling store is unavailable: %w", kernel.ErrStore)
	}
	ref := schedule.Reference{Kind: "cleanup:" + string(p.ItemType), Key: p.Key}
	if p.CronExp == "" {
		return s.removeSchedule(ctx, ref)
	}
	expression, err := schedule.Parse(p.CronExp, s.Zone)
	if err != nil {
		if !p.Enabled {
			// Emergency disablement must work even for an invalid legacy cron.
			// Removing its position invalidates already queued schedule work.
			return s.removeSchedule(ctx, ref)
		}
		return fmt.Errorf("cleanup schedule: %w", err)
	}
	state := schedule.State{Reference: ref, Revision: p.Revision, Cron: expression.String(), Zone: expression.Zone(), Enabled: p.Enabled}
	if p.Enabled {
		state.Next, err = expression.Next(s.now())
		if err != nil {
			return fmt.Errorf("cleanup schedule has no future occurrence: %w", kernel.ErrInvalid)
		}
	}
	if err := s.Schedules.Put(ctx, state); err != nil {
		return fmt.Errorf("publish cleanup schedule: %w", err)
	}
	return nil
}

func (s Service) removeSchedule(ctx context.Context, ref schedule.Reference) error {
	if err := s.Schedules.Delete(ctx, ref); err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("remove cleanup schedule: %w", err)
	}
	return nil
}

// WithPolicy admits a new run only for a currently enabled package policy and
// the exact queued revision. The callback records its durable run start under
// the same transaction/guard as disable, edit and deletion. Every queued run
// requires fresh admission; a run already admitted continues under its own run
// state, rather than consulting this enablement fence for each later batch.
// CLN-04 owns artifact authority, work leases and the explicit stop operation.
// The admission callback performs no network I/O or client streaming.
func (s Service) WithPolicy(ctx context.Context, key, revision string, effect func(context.Context, model.CleanupPolicy) error) (bool, error) {
	if s.Store == nil || s.Guard == nil || effect == nil {
		return false, fmt.Errorf("cleanup admission is unavailable: %w", kernel.ErrStore)
	}
	admitted := false
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return fmt.Errorf("guard cleanup admission: %w", err)
		}
		p, err := s.Store.Cleanup().Policy(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read cleanup admission: %w", err)
		}
		if p.ItemType != model.CleanupPackage || !p.Enabled || revision == "" || p.Revision != revision {
			return nil
		}
		if err := effect(ctx, p); err != nil {
			return fmt.Errorf("cleanup admitted effect: %w", err)
		}
		admitted = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("cleanup admission: %w", err)
	}
	return admitted, nil
}
