package storetest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	accessmodel "github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

type cleanupEnqueueFault struct{ work.WorkQueue }

var errCleanupEnqueue = errors.New("injected refusal after cleanup enqueue")

func (q cleanupEnqueueFault) Enqueue(ctx context.Context, kind, key string, payload []byte, now time.Time) error {
	if err := q.WorkQueue.Enqueue(ctx, kind, key, payload, now); err != nil {
		return err //nolint:wrapcheck // the queue names its write
	}
	return errCleanupEnqueue
}

func cleanupRunService(db Store, now func() time.Time) cleanup.Service {
	return cleanup.Service{
		Store: db, Work: db.WorkQueue(), Schedules: db.Schedules(), Guard: db.Projects().Guard,
		ProjectExists: func(ctx context.Context, key string) error {
			if _, err := db.Projects().Get(ctx, key); err != nil {
				return fmt.Errorf("current cleanup fixture project: %w", err)
			}
			return nil
		},
		ProjectIncarnation: func(ctx context.Context, key string) (string, error) {
			authority, err := db.Projects().Authority(ctx, key)
			if err != nil {
				return "", fmt.Errorf("current cleanup fixture lifetime: %w", err)
			}
			return string(authority.Incarnation), nil
		},
		Now: now,
		Authorize: func(ctx context.Context) (cleanup.Policy, error) {
			return cleanupAuthority{}, db.Projects().Guard(ctx)
		},
	}
}

func testCleanupRunAdmission(t *testing.T, db Store) {
	ctx := t.Context()
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	s := cleanupRunService(db, func() time.Time { return now })
	p := model.CleanupPolicy{
		Key: "admission", Revision: "initial-admission-revision", ItemType: model.CleanupPackage, Enabled: true,
		ExecutionCredential: (cleanupAuthority{}).Credential(),
		Criteria:            model.CleanupCriteria{PackageTypes: []string{"npm"}, Repos: []string{"**"}, IncludedPackages: []string{"**"}},
	}
	storekit.Must(t, db.Cleanup().CreatePolicy(ctx, p))
	s.Work = cleanupEnqueueFault{db.WorkQueue()}
	_, err := s.ManualRun(ctx, p.Key, true)
	storekit.WantErr(t, err, errCleanupEnqueue)
	runs, _, err := db.Cleanup().Runs(ctx, port.RunFilter{}, kernel.Page{})
	storekit.Must(t, err)
	if len(runs) != 0 {
		t.Fatal("queue refusal retained its run")
	}
	stats, err := db.WorkQueue().Stats(ctx)
	storekit.Must(t, err)
	for _, stat := range stats {
		if stat.Kind == cleanup.WorkKind && stat.Pending+stat.Leased+stat.Parked != 0 {
			t.Fatal("failed admission left executable work without a committed run")
		}
	}
	s.Work = db.WorkQueue()
	r, err := s.ManualRun(ctx, p.Key, true)
	storekit.Must(t, err)
	fresh := s // A fresh service uses only persisted references, not a request closure.
	e, err := fresh.Store.Cleanup().Execution(ctx, r.ID)
	storekit.Must(t, err)
	if e.Policy.Revision != p.Revision || e.Credential.TokenID != p.ExecutionCredential.TokenID || r.TriggerID != r.ID || !r.DryRun {
		t.Fatal("durable admission lost its policy/credential/trigger snapshot")
	}
	_, err = db.WorkQueue().Get(ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10))
	storekit.Must(t, err)
	storekit.Must(t, fresh.Stop(ctx, r.ID))

	// A final scheduler lease refusal must roll back BOTH the run and its own
	// executable queue item, even though both were written before the fence.
	p.Key, p.Revision, p.CronExp = "scheduled-admission", "scheduled-revision", "0 0 2 * * ?"
	storekit.Must(t, db.Cleanup().CreatePolicy(ctx, p))
	ref := schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key}
	storekit.Must(t, db.Schedules().Put(ctx, schedule.State{Reference: ref, Revision: p.Revision, Cron: p.CronExp, Zone: "UTC", Enabled: true, Next: now.Add(time.Hour)}))
	now = now.Add(2 * time.Hour)
	scheduler := schedule.Service{Store: db, Now: func() time.Time { return now }}
	count, err := scheduler.Tick(ctx, 1)
	storekit.Must(t, err)
	if count != 1 {
		t.Fatal("expected one scheduled occurrence")
	}
	leased, err := db.WorkQueue().Lease(ctx, work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: "cleanup-admission", Limit: 1, Now: now, Until: now.Add(time.Minute)})
	storekit.Must(t, err)
	if len(leased) != 1 {
		t.Fatal("logical occurrence was not queued")
	}
	err = scheduler.Execute(ctx, leased[0], map[string]schedule.Handler{cleanup.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
		now = now.Add(2 * time.Minute)
		_, err := s.ScheduledRun(ctx, trigger)
		if err != nil {
			return fmt.Errorf("admit scheduled cleanup under final lease: %w", err)
		}
		return nil
	}})
	storekit.WantErr(t, err, kernel.ErrConflict)
	_, err = db.Cleanup().ExecutionForOccurrence(ctx, leased[0].Key)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	runs, _, err = db.Cleanup().Runs(ctx, port.RunFilter{PolicyKey: p.Key}, kernel.Page{})
	storekit.Must(t, err)
	if len(runs) != 0 {
		t.Fatal("expired final lease retained a scheduled run")
	}
}

func testCleanupRunConcurrentAdmission(t *testing.T, db Store) {
	ctx := t.Context()
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	s := cleanupRunService(db, func() time.Time { return now })
	p := model.CleanupPolicy{
		Key: "concurrent-admission", Revision: "concurrent-revision", ItemType: model.CleanupPackage, Enabled: true,
		ExecutionCredential: (cleanupAuthority{}).Credential(),
	}
	storekit.Must(t, db.Cleanup().CreatePolicy(ctx, p))
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := s.ManualRun(ctx, p.Key, false)
			results <- err
		}()
	}
	close(start)
	admitted, refused := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, kernel.ErrInvalid):
			refused++
		default:
			t.Error("unexpected concurrent admission error", err)
		}
	}
	if admitted != 1 || refused != 1 {
		t.Fatal("concurrent requests did not serialize at one admitted policy run", admitted, refused)
	}
	runs, _, err := db.Cleanup().Runs(ctx, port.RunFilter{PolicyKey: p.Key}, kernel.Page{})
	storekit.Must(t, err)
	if len(runs) != 1 || runs[0].TriggerID != runs[0].ID {
		t.Fatal("concurrent admission lost or duplicated its stable integer trigger")
	}
}

func testCleanupRunProjectLifetime(t *testing.T, db Store) {
	ctx := t.Context()
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	s := cleanupRunService(db, func() time.Time { return now })
	storekit.Must(t, db.Projects().Create(ctx, accessmodel.Project{Key: "alpha"}))
	_, err := s.Create(ctx, "project-lifetime", model.CleanupPolicy{
		Key: "project-lifetime", ProjectKey: "alpha",
		Criteria: model.CleanupCriteria{PackageTypes: []string{"npm"}, Repos: []string{"**"}, IncludedPackages: []string{"**"}},
	})
	storekit.Must(t, err)
	storekit.Must(t, s.Enable(ctx, "project-lifetime", true))
	p, err := s.Get(ctx, "project-lifetime", false)
	storekit.Must(t, err)
	r, err := s.ManualRun(ctx, p.Key, false)
	storekit.Must(t, err)
	original, err := db.Cleanup().Execution(ctx, r.ID)
	storekit.Must(t, err)
	if original.ProjectIncarnation == "" || original.ProjectIncarnation != p.ProjectIncarnation {
		t.Fatal("project run was admitted without the policy's lifetime fence")
	}
	storekit.Must(t, db.Projects().Delete(ctx, "alpha"))
	storekit.Must(t, db.Projects().Create(ctx, accessmodel.Project{Key: "alpha"}))
	_, err = s.ManualRun(ctx, p.Key, false)
	storekit.WantErr(t, err, kernel.ErrConflict)
	storekit.Must(t, s.Enable(ctx, p.Key, true))
	fresh, err := s.Get(ctx, p.Key, false)
	storekit.Must(t, err)
	if fresh.ProjectIncarnation == original.ProjectIncarnation || fresh.Revision == p.Revision {
		t.Fatal("verified re-enablement reused the ended policy/project generation")
	}
	retained, err := db.Cleanup().Execution(ctx, r.ID)
	storekit.Must(t, err)
	storekit.Same(t, "project recreation retains the original run snapshot", original, retained)
}
