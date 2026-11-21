package cleanup_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

var errCleanupBoundary = errors.New("injected cleanup transaction boundary fault")

type cleanupBoundaryStore struct {
	port.Store
	cleanup port.CleanupStore
}

func TestUnitCleanupScheduledBoundaryFailuresCannotAdmitWork(t *testing.T) {
	for _, boundary := range []string{"guard", "policy", "restore", "restore nil", "authorize", "authority nil", "authority denied"} {
		t.Run(boundary, func(t *testing.T) {
			s, db := world(t, "*")
			s.Work = db.WorkQueue()
			ctx := t.Context()
			p, err := s.Create(ctx, "scheduled-boundary", input("scheduled-boundary", "alpha"))
			must(t, err)
			must(t, s.Enable(ctx, p.Key, true))
			p, err = s.Get(ctx, p.Key, false)
			must(t, err)
			want := errCleanupBoundary
			switch boundary {
			case "guard":
				s.Guard = func(context.Context) error { return errCleanupBoundary }
			case "policy":
				s.Store = cleanupBoundaryStore{Store: db, cleanup: storetest.CleanupFaults{CleanupStore: db.Cleanup(), After: func(_ context.Context, method string) error {
					if method == "Policy" {
						return errCleanupBoundary
					}
					return nil
				}}}
			case "restore":
				s.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) {
					return nil, errCleanupBoundary
				}
			case "restore nil":
				s.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) { return nil, nil }
				want = kernel.ErrStore
			case "authorize":
				s.Authorize = func(context.Context) (cleanup.Policy, error) { return nil, errCleanupBoundary }
			case "authority nil":
				s.Authorize = func(context.Context) (cleanup.Policy, error) { return nil, nil }
				want = kernel.ErrForbidden
			case "authority denied":
				s.Authorize = func(context.Context) (cleanup.Policy, error) { return policyAuthority("beta"), nil }
				want = kernel.ErrForbidden
			}
			trigger := schedule.Trigger{
				Reference: schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key},
				Revision:  p.Revision,
				ID:        "boundary-fire",
				Fire:      s.Now(),
				Fence:     func(context.Context) error { return nil },
			}
			r, err := s.ScheduledRun(ctx, trigger)
			if r.ID != 0 || !errors.Is(err, want) {
				t.Fatal("scheduled boundary failure admitted a run", r.ID, err)
			}
			runs, _, err := db.Cleanup().Runs(ctx, port.RunFilter{}, kernel.Page{})
			must(t, err)
			if len(runs) != 0 {
				t.Fatal("failed scheduler admission retained run state")
			}
		})
	}
}

func TestUnitCleanupScheduledReferenceValidationPrecedesAdmission(t *testing.T) {
	for _, field := range []string{"kind", "identity", "revision", "fire", "fence", "deleted policy"} {
		t.Run(field, func(t *testing.T) {
			s, db := world(t, "*")
			s.Work = db.WorkQueue()
			p, err := s.Create(t.Context(), "invalid-fire", input("invalid-fire", "alpha"))
			must(t, err)
			trigger := schedule.Trigger{
				Reference: schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key},
				Revision:  p.Revision,
				ID:        "invalid-fire",
				Fire:      s.Now(),
				Fence:     func(context.Context) error { t.Fatal("invalid reference reached publication"); return nil },
			}
			want := kernel.ErrInvalid
			switch field {
			case "kind":
				trigger.Reference.Kind = "different"
			case "identity":
				trigger.ID = ""
			case "revision":
				trigger.Revision = ""
			case "fire":
				trigger.Fire = time.Time{}
			case "fence":
				trigger.Fence = nil
			case "deleted policy":
				trigger.Reference.Key = "absent"
				want = nil
			}
			r, err := s.ScheduledRun(t.Context(), trigger)
			if r.ID != 0 || !errors.Is(err, want) {
				t.Fatal("invalid occurrence admitted work", r.ID, err)
			}
		})
	}
}

func TestUnitCleanupActiveExecutionReadFailureBlocksPolicyChanges(t *testing.T) {
	for _, action := range []string{"update", "delete", "force disable"} {
		t.Run(action, func(t *testing.T) {
			s, db := world(t, "*")
			s.Work = db.WorkQueue()
			ctx := t.Context()
			p, err := s.Create(ctx, "active-boundary", input("active-boundary", "alpha"))
			must(t, err)
			r, err := s.ManualRun(ctx, p.Key, true)
			must(t, err)
			s.Store = cleanupBoundaryStore{Store: db, cleanup: storetest.CleanupFaults{CleanupStore: db.Cleanup(), After: func(_ context.Context, method string) error {
				if method == "Execution" {
					return errCleanupBoundary
				}
				return nil
			}}}
			switch action {
			case "update":
				_, err = s.Update(ctx, p.Key, p)
			case "delete":
				err = s.Delete(ctx, p.Key)
			default:
				err = s.DisableAll(ctx, p.ProjectKey)
			}
			if !errors.Is(err, errCleanupBoundary) {
				t.Fatal("failed execution read was mistaken for absent work", err)
			}
			stored, err := db.Cleanup().Run(ctx, r.ID)
			must(t, err)
			if stored.Status != api.RunInProgress {
				t.Fatal("failed scope lookup stopped an unverified run")
			}
			storedPolicy, err := db.Cleanup().Policy(ctx, p.Key)
			must(t, err)
			if !reflect.DeepEqual(p, storedPolicy) {
				t.Fatal("failed scope lookup changed policy")
			}
		})
	}
}

func (s cleanupBoundaryStore) Cleanup() port.CleanupStore { return s.cleanup }

type cleanupEnqueueBoundary struct{ work.WorkQueue }

func (q cleanupEnqueueBoundary) Enqueue(ctx context.Context, kind, key string, payload []byte, at time.Time) error {
	if err := q.WorkQueue.Enqueue(ctx, kind, key, payload, at); err != nil {
		return err
	}
	return errCleanupBoundary
}

func TestUnitCleanupAdmissionBoundariesRollbackAndRetry(t *testing.T) {
	for _, operation := range []string{"Policy", "Runs", "CreateRun", "UpdateRun", "CreateExecution", "CreateProgress", "Enqueue"} {
		t.Run(operation, func(t *testing.T) {
			s, db := world(t, "*")
			ctx := t.Context()
			_, err := s.Create(ctx, "admission-boundary", input("admission-boundary", "alpha"))
			must(t, err)
			s.Work = db.WorkQueue()
			hit := false
			s.Store = cleanupBoundaryStore{Store: db, cleanup: storetest.CleanupFaults{CleanupStore: db.Cleanup(), After: func(_ context.Context, method string) error {
				if method == operation {
					hit = true
					return errCleanupBoundary
				}
				return nil
			}}}
			if operation == "Enqueue" {
				s.Work = cleanupEnqueueBoundary{db.WorkQueue()}
				hit = true
			}
			r, err := s.ManualRun(ctx, "admission-boundary", true)
			if !hit || !errors.Is(err, errCleanupBoundary) || r.ID != 0 {
				t.Fatal("failed boundary published a trigger", operation, r.ID, err)
			}
			runs, _, err := db.Cleanup().Runs(ctx, port.RunFilter{}, kernel.Page{})
			must(t, err)
			if len(runs) != 0 {
				t.Fatal("failed admission retained a run")
			}
			stats, err := db.WorkQueue().Stats(ctx)
			must(t, err)
			for _, stat := range stats {
				if stat.Kind == cleanup.WorkKind {
					t.Fatal("failed admission left executable work")
				}
			}
			s.Store, s.Work = db, db.WorkQueue()
			r, err = s.ManualRun(ctx, "admission-boundary", true)
			must(t, err)
			if r.ID <= 0 || r.TriggerID != r.ID {
				t.Fatal("retry lost its durable integer identity")
			}
			_, err = db.Cleanup().Execution(ctx, r.ID)
			must(t, err)
			_, err = db.Cleanup().Progress(ctx, r.ID)
			must(t, err)
			_, err = db.WorkQueue().Get(ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10))
			must(t, err)
		})
	}
}

func TestUnitCleanupStopBoundariesPreserveRunAndWork(t *testing.T) {
	for _, tc := range []struct {
		operation string
		nth       int
		all       bool
	}{
		{operation: "Execution", nth: 1},
		{operation: "Execution", nth: 2},
		{operation: "Run", nth: 1},
		{operation: "UpdateRun", nth: 1},
		{operation: "Runs", nth: 1, all: true},
		{operation: "Enqueue", nth: 1},
	} {
		t.Run(tc.operation+strconv.Itoa(tc.nth), func(t *testing.T) {
			s, db := world(t, "*")
			ctx := t.Context()
			s.Work = db.WorkQueue()
			_, err := s.Create(ctx, "stop-boundary", input("stop-boundary", "alpha"))
			must(t, err)
			r, err := s.ManualRun(ctx, "stop-boundary", true)
			must(t, err)
			beforeRun, err := db.Cleanup().Run(ctx, r.ID)
			must(t, err)
			beforeWork, err := db.WorkQueue().Get(ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10))
			must(t, err)
			calls := 0
			s.Store = cleanupBoundaryStore{Store: db, cleanup: storetest.CleanupFaults{CleanupStore: db.Cleanup(), After: func(_ context.Context, method string) error {
				if method == tc.operation {
					calls++
					if calls == tc.nth {
						return errCleanupBoundary
					}
				}
				return nil
			}}}
			if tc.operation == "Enqueue" {
				s.Work = cleanupEnqueueBoundary{db.WorkQueue()}
			}
			if tc.all {
				err = s.StopAll(ctx)
			} else {
				err = s.Stop(ctx, r.ID)
			}
			if !errors.Is(err, errCleanupBoundary) {
				t.Fatal("stop ignored a transaction boundary failure", err)
			}
			afterRun, err := db.Cleanup().Run(ctx, r.ID)
			must(t, err)
			afterWork, err := db.WorkQueue().Get(ctx, cleanup.WorkKind, beforeWork.Key)
			must(t, err)
			if !reflect.DeepEqual(beforeRun, afterRun) || !reflect.DeepEqual(beforeWork, afterWork) {
				t.Fatal("failed stop changed run/work state")
			}
			s.Store, s.Work = db, db.WorkQueue()
			must(t, s.Stop(ctx, r.ID))
			afterRun, err = db.Cleanup().Run(ctx, r.ID)
			must(t, err)
			if afterRun.Status != api.RunStopping || !afterRun.Ended.IsZero() {
				t.Fatal("retry falsely claimed terminal settlement")
			}
		})
	}
}

func TestUnitCleanupReplayReadFailuresReturnNoTrigger(t *testing.T) {
	for _, operation := range []string{"Policy", "Execution", "Run"} {
		t.Run(operation, func(t *testing.T) {
			s, db := world(t, "*")
			ctx := t.Context()
			s.Work = db.WorkQueue()
			_, err := s.Create(ctx, "replay-boundary", input("replay-boundary", "alpha"))
			must(t, err)
			r, err := s.ManualRun(ctx, "replay-boundary", true)
			must(t, err)
			s.Store = cleanupBoundaryStore{Store: db, cleanup: storetest.CleanupFaults{CleanupStore: db.Cleanup(), After: func(_ context.Context, method string) error {
				if method == operation {
					return errCleanupBoundary
				}
				return nil
			}}}
			trigger, err := s.RunTrigger(ctx, "replay-boundary", r.ID)
			if !errors.Is(err, errCleanupBoundary) || trigger != 0 {
				t.Fatal("failed replay lookup disclosed an unconfirmed trigger", trigger, err)
			}
			s.Store = db
			trigger, err = s.RunTrigger(ctx, "replay-boundary", r.ID)
			must(t, err)
			if trigger != r.ID {
				t.Fatal("retry changed the original trigger")
			}
		})
	}
}
