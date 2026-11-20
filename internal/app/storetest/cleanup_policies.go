package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

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

func testCleanupPolicyGenerations(t *testing.T, db Store) {
	ctx := t.Context()
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	s := cleanup.Service{
		Store: db, Schedules: db.Schedules(), Repositories: db.Repos(), Guard: db.Projects().Guard,
		Now: func() time.Time { return now },
		Authorize: func(ctx context.Context, _ kernel.ScopeActions) (cleanup.Policy, error) {
			return cleanupAuthority{}, db.Projects().Guard(ctx)
		},
	}
	key := "cleanup-generations"
	in := model.CleanupPolicy{Key: key, CronExp: "0 0 2 * * ?", Criteria: model.CleanupCriteria{
		PackageTypes: []string{"npm"}, Repos: []string{"**"}, IncludedPackages: []string{"**"},
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
