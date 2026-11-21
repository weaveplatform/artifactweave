package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

type cleanupAuthority struct{}

func (cleanupAuthority) Any() bool          { return true }
func (cleanupAuthority) Allows(string) bool { return true }
func (cleanupAuthority) Name() string       { return "admin" }
func (cleanupAuthority) Credential() kernel.CredentialReference {
	return kernel.CredentialReference{Name: "admin", TokenID: "verified-test-token"}
}

func testCleanupPolicyGenerations(t *testing.T, db Store) {
	ctx := t.Context()
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	s := cleanup.Service{
		Store: db, Schedules: db.Schedules(), Repositories: db.Repos(), Guard: db.Projects().Guard,
		Now: func() time.Time { return now },
		Authorize: func(ctx context.Context) (cleanup.Policy, error) {
			return cleanupAuthority{}, db.Projects().Guard(ctx)
		},
	}
	key := "cleanup-generations"
	in := model.CleanupPolicy{Key: key, CronExp: "0 0 2 * * ?", Criteria: model.CleanupCriteria{
		PackageTypes: []string{"npm"}, Repos: []string{"**"}, IncludedPackages: []string{"**"},
		ExcludedRepos: []string{}, IncludedProperties: map[string][]string{},
	}}
	_, err := s.Create(ctx, key, in)
	storekit.Must(t, err)
	storekit.Must(t, s.Enable(ctx, key, true))
	active, err := s.Get(ctx, key, false)
	storekit.Must(t, err)
	now = now.Add(2 * time.Hour)
	scheduler := schedule.Service{Store: db, Now: func() time.Time { return now }}
	n, err := scheduler.Tick(ctx, 1)
	storekit.Must(t, err)
	if n != 1 {
		t.Fatalf("enabled cleanup schedule queued %d occurrences, want 1", n)
	}
	items, err := db.WorkQueue().Lease(ctx, work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: "cleanup-worker", Limit: 1, Now: now, Until: now.Add(time.Minute)})
	storekit.Must(t, err)
	if len(items) != 1 {
		t.Fatalf("queued cleanup occurrence missing: %+v", items)
	}
	position, err := db.Schedules().Get(ctx, schedule.Reference{Kind: cleanup.ScheduleKind, Key: key})
	storekit.Must(t, err)
	storekit.Must(t, s.Enable(ctx, key, true))
	unchanged, err := s.Update(ctx, key, in)
	storekit.Must(t, err)
	afterRetry, err := db.Schedules().Get(ctx, position.Reference)
	storekit.Must(t, err)
	storekit.Same(t, "identical update/enablement preserve logical schedule", position, afterRetry)
	if unchanged.Revision != active.Revision {
		t.Fatal("SQL empty-collection roundtrip changed an identical policy generation")
	}
	admittedRetry := false
	storekit.Must(t, scheduler.Execute(ctx, items[0], map[string]schedule.Handler{cleanup.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
		var err error
		admittedRetry, err = s.WithPolicy(ctx, trigger.Reference.Key, trigger.Revision, func(ctx context.Context, p model.CleanupPolicy) error {
			if _, err := db.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, ItemType: p.ItemType, Status: api.RunCompleted}); err != nil {
				return fmt.Errorf("persist retry-admitted run: %w", err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("admit queued occurrence after retry: %w", err)
		}
		return nil
	}}))
	if !admittedRetry {
		t.Fatal("identical retry discarded the queued occurrence")
	}
	storekit.Must(t, s.DisableAll(ctx, ""))
	effects := 0
	handlers := map[string]schedule.Handler{cleanup.ScheduleKind: func(context.Context, schedule.Trigger) error { effects++; return nil }}
	storekit.Must(t, scheduler.Execute(ctx, items[0], handlers))
	if effects != 0 {
		t.Fatal("durably queued occurrence ran after force disable")
	}
	storekit.Must(t, s.Enable(ctx, key, true))
	storekit.Must(t, scheduler.Execute(ctx, items[0], handlers))
	if effects != 0 {
		t.Fatal("re-enabling revived an old queued occurrence")
	}
	admitted, err := s.WithPolicy(ctx, key, active.Revision, func(context.Context, model.CleanupPolicy) error { effects++; return nil })
	storekit.Must(t, err)
	if admitted || effects != 0 {
		t.Fatal("old policy generation admitted a later local publication")
	}
	current, err := s.Get(ctx, key, false)
	storekit.Must(t, err)
	admitted, err = s.WithPolicy(ctx, key, current.Revision, func(ctx context.Context, p model.CleanupPolicy) error {
		_, err := db.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, ItemType: p.ItemType, Status: api.RunCompleted})
		if err != nil {
			return fmt.Errorf("create admitted cleanup run: %w", err)
		}
		return nil
	})
	storekit.Must(t, err)
	if !admitted {
		t.Fatal("current enabled policy refused bounded publication")
	}
	storekit.Must(t, s.Delete(ctx, key))
	_, err = db.Cleanup().LastRun(ctx, key)
	storekit.Must(t, err)
	_, err = db.Schedules().Get(ctx, schedule.Reference{Kind: cleanup.ScheduleKind, Key: key})
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = s.Create(ctx, key, in)
	storekit.Must(t, err)
	storekit.Must(t, s.Enable(ctx, key, true))
	admitted, err = s.WithPolicy(ctx, key, current.Revision, func(context.Context, model.CleanupPolicy) error { effects++; return nil })
	storekit.Must(t, err)
	if admitted {
		t.Fatal("delete/recreate revived old policy generation")
	}
}

type cleanupRepositoryLookup func(context.Context, string) (artifactsapi.ProjectRepository, error)

func (lookup cleanupRepositoryLookup) ProjectRepository(ctx context.Context, key string) (artifactsapi.ProjectRepository, error) {
	return lookup(ctx, key)
}

func testCleanupPolicyConcurrentDisable(t *testing.T, db Store) {
	ctx := t.Context()
	storekit.Must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "concurrent-npm", Class: repo.Local, PackageType: repo.NPM})))
	s := cleanup.Service{
		Store: db, Schedules: db.Schedules(), Repositories: db.Repos(), Guard: db.Projects().Guard,
		Authorize: func(ctx context.Context) (cleanup.Policy, error) { return cleanupAuthority{}, db.Projects().Guard(ctx) },
	}
	p := model.CleanupPolicy{
		Key:      "concurrent-cleanup",
		Criteria: model.CleanupCriteria{PackageTypes: []string{"npm"}, Repos: []string{"concurrent-npm"}, IncludedPackages: []string{"**"}},
	}
	_, err := s.Create(ctx, p.Key, p)
	storekit.Must(t, err)
	storekit.Must(t, s.Enable(ctx, p.Key, true))
	atRead, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	updater := s
	updater.Repositories = cleanupRepositoryLookup(func(ctx context.Context, key string) (artifactsapi.ProjectRepository, error) {
		close(atRead)
		select {
		case <-release:
		case <-ctx.Done():
			return artifactsapi.ProjectRepository{}, fmt.Errorf("blocked cleanup lookup: %w", ctx.Err())
		}
		r, err := db.Repos().ProjectRepository(ctx, key)
		if err != nil {
			return r, fmt.Errorf("lookup cleanup repository: %w", err)
		}
		return r, nil
	})
	updated := make(chan error, 1)
	p.Description = "concurrent edit"
	go func() { _, err := updater.Update(ctx, p.Key, p); updated <- err }()
	select {
	case <-atRead:
	case <-time.After(5 * time.Second):
		t.Fatal("update did not reach guarded configuration validation")
	}
	started, disabled := make(chan struct{}), make(chan error, 1)
	go func() { close(started); disabled <- s.DisableAll(ctx, "") }()
	<-started
	select {
	case err := <-disabled:
		t.Fatalf("disable passed a transaction holding the policy snapshot: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// The update owns the pre-disable enabled snapshot. Releasing its guarded
	// validation must let it finish first, then disable must win without losing
	// the edited configuration on either SQL engine.
	release <- struct{}{}
	select {
	case err := <-updated:
		storekit.Must(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("guarded update did not complete")
	}
	select {
	case err := <-disabled:
		storekit.Must(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("disable did not complete after the guarded update")
	}
	current, err := s.Get(ctx, p.Key, false)
	storekit.Must(t, err)
	if current.Enabled || current.Description != p.Description || current.Revision == "" {
		t.Fatalf("concurrent disable/update lost state: %+v", current)
	}
}
