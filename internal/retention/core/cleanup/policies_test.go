package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	accessmodel "github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

type policyAuthority string

func (a policyAuthority) Any() bool                  { return a != "none" }
func (a policyAuthority) Allows(project string) bool { return a == "*" || string(a) == project }
func (policyAuthority) Name() string                 { return "admin" }
func (policyAuthority) Credential() kernel.CredentialReference {
	return kernel.CredentialReference{Name: "admin", TokenID: "verified-test-token"}
}

func world(t *testing.T, authority string) (cleanup.Service, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	for _, key := range []string{"alpha", "beta"} {
		must(t, db.Projects().Create(t.Context(), accessmodel.Project{Key: key}))
		must(t, db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: key + "-npm", Class: repo.Local, PackageType: repo.NPM, ProjectKey: key})))
	}
	s := cleanup.Service{
		Store: db, Schedules: db.Schedules(), Repositories: db.Repos(), Guard: db.Projects().Guard,
		Now:           func() time.Time { return time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC) },
		ProjectExists: func(ctx context.Context, key string) error { _, err := db.Projects().Get(ctx, key); return err },
		Authorize: func(ctx context.Context) (cleanup.Policy, error) {
			if err := db.Projects().Guard(ctx); err != nil {
				return nil, err
			}
			return policyAuthority(authority), nil
		},
	}
	return s, db
}

func input(key, project string) model.CleanupPolicy {
	return model.CleanupPolicy{Key: key, ProjectKey: project, CronExp: "0 0 2 * * ?", Criteria: model.CleanupCriteria{
		PackageTypes: []string{"npm"}, Repos: []string{"**"}, IncludedPackages: []string{"@scope/*"},
		ExcludedPackages: []string{"@scope/keep"}, CreatedBeforeInMonths: 2,
		IncludedProperties: map[string][]string{"retention": {"clean"}},
	}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnitPolicyLifecycleFencesQueuedGenerationsAndKeepsHistory(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	p, err := s.Create(ctx, "cleanup-one", input("cleanup-one", "alpha"))
	must(t, err)
	if p.Enabled || p.Revision == "" || p.ItemType != model.CleanupPackage || p.CreatedBy != "admin" {
		t.Fatalf("create must be inactive with server-owned audit/revision: %+v", p)
	}
	ref := schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key}
	state, err := db.Schedules().Get(ctx, ref)
	must(t, err)
	if state.Enabled || !state.Next.IsZero() || state.Revision != p.Revision {
		t.Fatalf("inactive schedule differs: %+v", state)
	}
	must(t, s.Enable(ctx, p.Key, true))
	active, err := s.Get(ctx, p.Key, false)
	must(t, err)
	state, err = db.Schedules().Get(ctx, ref)
	must(t, err)
	if !active.Enabled || active.Revision == p.Revision || state.Revision != active.Revision || !state.Next.Equal(s.Now().Add(time.Hour)) {
		t.Fatalf("enable did not publish new position: policy=%+v schedule=%+v", active, state)
	}
	effects := 0
	effect := func(context.Context, model.CleanupPolicy) error { effects++; return nil }
	admitted, err := s.WithPolicy(ctx, p.Key, active.Revision, effect)
	must(t, err)
	if !admitted || effects != 1 {
		t.Fatal("current enabled generation was not admitted")
	}
	replacement := input(p.Key, "alpha")
	replacement.Description = "criteria-only edit"
	replacement.Enabled = false // replace cannot disable a running policy
	edited, err := s.Update(ctx, p.Key, replacement)
	must(t, err)
	if !edited.Enabled || edited.Revision == active.Revision || !edited.Created.Equal(p.Created) {
		t.Fatal("replacement changed enablement/creation or reused generation")
	}
	for _, revision := range []string{p.Revision, active.Revision, ""} {
		admitted, err = s.WithPolicy(ctx, p.Key, revision, effect)
		must(t, err)
		if admitted {
			t.Fatal("stale generation executed after edit")
		}
	}
	must(t, s.DisableAll(ctx, "alpha"))
	admitted, err = s.WithPolicy(ctx, p.Key, edited.Revision, effect)
	must(t, err)
	if admitted || effects != 1 {
		t.Fatal("disabled queued work executed")
	}
	run, err := db.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, ItemType: model.CleanupPackage, Status: api.RunCompleted})
	must(t, err)
	must(t, s.Delete(ctx, p.Key))
	_, err = db.Cleanup().Run(ctx, run.ID)
	must(t, err)
	_, err = db.Schedules().Get(ctx, ref)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("delete kept schedule: %v", err)
	}
	recreated, err := s.Create(ctx, p.Key, input(p.Key, "alpha"))
	must(t, err)
	must(t, s.Enable(ctx, p.Key, true))
	if recreated.Revision == edited.Revision {
		t.Fatal("recreated policy reused generation")
	}
	admitted, err = s.WithPolicy(ctx, p.Key, edited.Revision, effect)
	must(t, err)
	if admitted {
		t.Fatal("recreation revived deleted queued work")
	}
}

func TestUnitPolicyIdenticalEnableAndUpdateKeepQueuedOccurrence(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	now := s.Now()
	s.Now = func() time.Time { return now }
	in := input("idempotent-policy", "")
	_, err := s.Create(ctx, in.Key, in)
	must(t, err)
	must(t, s.Enable(ctx, in.Key, true))
	active, err := s.Get(ctx, in.Key, false)
	must(t, err)
	now = now.Add(2 * time.Hour)
	scheduler := schedule.Service{Store: db, Now: func() time.Time { return now }}
	queued, err := scheduler.Tick(ctx, 1)
	must(t, err)
	if queued != 1 {
		t.Fatal("daily occurrence was not queued")
	}
	items, err := db.WorkQueue().Lease(ctx, work.WorkLease{Holder: "idempotence", Kinds: []string{schedule.WorkKind}, Limit: 1, Now: now, Until: now.Add(time.Minute)})
	must(t, err)
	if len(items) != 1 {
		t.Fatal("queued occurrence was not leased")
	}
	before, err := db.Schedules().Get(ctx, schedule.Reference{Kind: cleanup.ScheduleKind, Key: in.Key})
	must(t, err)
	must(t, s.Enable(ctx, in.Key, true))
	updated, err := s.Update(ctx, in.Key, in)
	must(t, err)
	after, err := db.Schedules().Get(ctx, before.Reference)
	must(t, err)
	if updated.Revision != active.Revision || !reflect.DeepEqual(after, before) {
		t.Fatal("identical enablement/configuration changed the queued generation or logical position")
	}
	admitted := false
	err = scheduler.Execute(ctx, items[0], map[string]schedule.Handler{cleanup.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
		var err error
		admitted, err = s.WithPolicy(ctx, trigger.Reference.Key, trigger.Revision, func(ctx context.Context, p model.CleanupPolicy) error {
			_, err := db.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, ItemType: p.ItemType, Status: api.RunInProgress})
			if err != nil {
				return fmt.Errorf("persist admitted cleanup run: %w", err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("admit unchanged cleanup policy: %w", err)
		}
		return nil
	}})
	must(t, err)
	if !admitted {
		t.Fatal("retry silently discarded the queued daily occurrence")
	}
	_, err = db.Cleanup().LastRun(ctx, in.Key)
	must(t, err)
}

func TestUnitPolicyProjectFiltersAndRepositoryCriteriaStayBounded(t *testing.T) {
	admin, db := world(t, "*")
	ctx := t.Context()
	for _, project := range []string{"", "alpha", "beta"} {
		key := "cleanup-" + project
		_, err := admin.Create(ctx, key, input(key, project))
		must(t, err)
		must(t, admin.Enable(ctx, key, true))
	}
	project := admin
	project.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		return policyAuthority("alpha"), db.Projects().Guard(ctx)
	}
	all, err := project.List(ctx, "alpha")
	must(t, err)
	if len(all) != 1 || all[0].ProjectKey != "alpha" {
		t.Fatalf("project list widened: %+v", all)
	}
	for _, scope := range []string{"", "beta"} {
		_, err = project.List(ctx, scope)
		if !errors.Is(err, kernel.ErrForbidden) {
			t.Fatalf("project list accepted %q: %v", scope, err)
		}
		if err := project.DisableAll(ctx, scope); !errors.Is(err, kernel.ErrForbidden) {
			t.Fatalf("project force-disable accepted %q: %v", scope, err)
		}
	}
	for _, key := range []string{"cleanup-", "cleanup-beta"} {
		_, err = project.Get(ctx, key, false)
		if !errors.Is(err, kernel.ErrForbidden) {
			t.Fatalf("foreign get accepted: %v", err)
		}
	}
	foreign := input("foreign-repository", "alpha")
	foreign.Criteria.Repos = []string{"beta-npm"}
	_, err = project.Create(ctx, foreign.Key, foreign)
	if !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("foreign repository accepted: %v", err)
	}
	must(t, admin.DisableAll(ctx, ""))
	for _, key := range []string{"cleanup-alpha", "cleanup-beta"} {
		p, err := admin.Get(ctx, key, false)
		must(t, err)
		if !p.Enabled {
			t.Fatalf("global force-disable crossed into project: %q", key)
		}
	}
	global, err := admin.List(ctx, "")
	must(t, err)
	if len(global) != 1 || global[0].Enabled {
		t.Fatalf("global collection was not global-only/disabled: %+v", global)
	}
}

var errInjected = errors.New("injected schedule persistence failure")

func TestUnitPolicyEmergencyDisableToleratesInvalidLegacyConfiguration(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	for _, itemType := range []model.CleanupItemType{model.CleanupPackage, model.CleanupBuild, model.CleanupReleaseBundle} {
		p := model.CleanupPolicy{Key: "legacy-" + string(itemType), ItemType: itemType, Enabled: true, CronExp: "invalid legacy cron"}
		must(t, db.Cleanup().CreatePolicy(ctx, p))
	}
	must(t, s.DisableAll(ctx, ""))
	for _, itemType := range []model.CleanupItemType{model.CleanupPackage, model.CleanupBuild, model.CleanupReleaseBundle} {
		p, err := db.Cleanup().Policy(ctx, "legacy-"+string(itemType))
		must(t, err)
		if p.Enabled || p.Revision == "" {
			t.Fatalf("emergency disable left legacy policy eligible: %+v", p)
		}
	}
}

type failingSchedule struct{ schedule.Store }

func (f failingSchedule) Put(ctx context.Context, state schedule.State) error {
	if err := f.Store.Put(ctx, state); err != nil {
		return err
	}
	return errInjected
}

func TestUnitPolicyScheduleFailureRollsBackBothRepresentations(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	p, err := s.Create(ctx, "rollback-policy", input("rollback-policy", ""))
	must(t, err)
	before, err := db.Schedules().Get(ctx, schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key})
	must(t, err)
	s.Schedules = failingSchedule{s.Schedules}
	if err := s.Enable(ctx, p.Key, true); !errors.Is(err, errInjected) {
		t.Fatalf("injected failure was hidden: %v", err)
	}
	stored, err := s.Get(ctx, p.Key, false)
	must(t, err)
	after, err := db.Schedules().Get(ctx, before.Reference)
	must(t, err)
	if !reflect.DeepEqual(stored, p) || !reflect.DeepEqual(after, before) {
		t.Fatal("failed schedule publication left policy or schedule changed")
	}
	_, err = s.Create(ctx, "rollback-new", input("rollback-new", ""))
	if !errors.Is(err, errInjected) {
		t.Fatalf("create did not return schedule failure: %v", err)
	}
	_, err = db.Cleanup().Policy(ctx, "rollback-new")
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("failed create left policy row: %v", err)
	}
}
