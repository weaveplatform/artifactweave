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
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

type refusingRunQueue struct{ work.WorkQueue }

var errRunEnqueue = errors.New("injected cleanup queue refusal")

func (refusingRunQueue) Enqueue(context.Context, string, string, []byte, time.Time) error {
	return errRunEnqueue
}

func TestUnitCleanupManualAdmissionRollsBackWithQueueAndRetainsFrozenHistory(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	_, err := s.Create(ctx, "run-policy", input("run-policy", "alpha"))
	must(t, err)
	must(t, s.Enable(ctx, "run-policy", true))
	s.Work = refusingRunQueue{db.WorkQueue()}
	failed, err := s.ManualRun(ctx, "run-policy", true)
	if !errors.Is(err, errRunEnqueue) || failed.ID != 0 {
		t.Fatal("enqueue failure did not refuse the entire admission", err)
	}
	runs, _, err := db.Cleanup().Runs(ctx, port.RunFilter{}, kernel.Page{})
	must(t, err)
	if len(runs) != 0 {
		t.Fatal("failed enqueue retained a trigger/run")
	}
	s.Work = db.WorkQueue()
	r, err := s.ManualRun(ctx, "run-policy", true)
	must(t, err)
	public, err := cleanup.PublicRunID(r.ID)
	must(t, err)
	if int64(public) != r.ID || r.TriggerID != r.ID || !r.Manual || !r.DryRun || r.Status != api.RunInProgress || !r.Started.IsZero() {
		t.Fatal("admission lost integer identity, dry-run or queued state")
	}
	e, err := db.Cleanup().Execution(ctx, r.ID)
	must(t, err)
	if e.Policy.ProjectKey != "alpha" || e.Policy.Revision == "" || !e.At.Equal(s.Now()) ||
		!reflect.DeepEqual(e.Credential, policyAuthority("*").Credential()) || e.Policy.ExecutionCredential.Name != "" {
		t.Fatal("run did not retain its frozen policy/clock and original credential")
	}
	queued, err := db.WorkQueue().Get(ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10))
	must(t, err)
	if len(queued.Payload) != 0 || queued.State != work.WorkPending {
		t.Fatal("queue must contain only a run reference")
	}
	if _, err := s.ManualRun(ctx, "run-policy", false); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("overlapping policy run was admitted", err)
	}
	changed := input("run-policy", "alpha")
	changed.Description = "updated after admission"
	_, err = s.Update(ctx, "run-policy", changed)
	must(t, err)
	after, err := db.Cleanup().Execution(ctx, r.ID)
	must(t, err)
	if !reflect.DeepEqual(e, after) {
		t.Fatal("policy edit rewrote admitted execution state")
	}
	must(t, s.Delete(ctx, "run-policy"))
	must(t, s.Stop(ctx, r.ID))
	stopping, err := db.Cleanup().Run(ctx, r.ID)
	must(t, err)
	if stopping.Status != api.RunStopping || stopping.Status.Terminal() || !stopping.Ended.IsZero() {
		t.Fatal("stop claimed terminal completion before the executor fenced effects")
	}
}

func TestUnitCleanupDistinctRunsNeverCoalesceAndEmergencyStopsKeepScope(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	for _, project := range []string{"alpha", "beta"} {
		key := project + "-runs"
		_, err := s.Create(ctx, key, input(key, project))
		must(t, err)
		must(t, s.Enable(ctx, key, true))
	}
	first, err := s.ManualRun(ctx, "alpha-runs", false)
	must(t, err)
	first.Status = api.RunCompleted
	must(t, db.Cleanup().UpdateRun(ctx, first))
	second, err := s.ManualRun(ctx, "alpha-runs", false)
	must(t, err)
	foreign, err := s.ManualRun(ctx, "beta-runs", false)
	must(t, err)
	if second.ID == first.ID {
		t.Fatal("distinct manual runs reused one logical identity")
	}
	for _, id := range []int64{first.ID, second.ID, foreign.ID} {
		_, err := db.WorkQueue().Get(ctx, cleanup.WorkKind, strconv.FormatInt(id, 10))
		must(t, err)
	}
	project := s
	project.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		return policyAuthority("alpha"), db.Projects().Guard(ctx)
	}
	if err := project.Stop(ctx, foreign.ID); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("project administrator stopped a foreign run", err)
	}
	if err := project.StopAll(ctx); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("project administrator used platform force-stop", err)
	}
	must(t, project.DisableAll(ctx, "alpha"))
	for _, expected := range []model.CleanupRun{{ID: first.ID, Status: api.RunCompleted}, {ID: second.ID, Status: api.RunStopping}, {ID: foreign.ID, Status: api.RunInProgress}} {
		actual, err := db.Cleanup().Run(ctx, expected.ID)
		must(t, err)
		if actual.Status != expected.Status {
			t.Fatal("force-disable changed terminal/foreign run state", actual.Status)
		}
	}
	must(t, s.StopAll(ctx))
	actual, err := db.Cleanup().Run(ctx, foreign.ID)
	must(t, err)
	if actual.Status != api.RunStopping {
		t.Fatal("platform force-stop omitted an active project run")
	}
}

func TestUnitCleanupScheduledAdmissionDeduplicatesAndFencesItsLease(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(strconv.FormatBool(expired), func(t *testing.T) {
			s, db := world(t, "*")
			s.Work = db.WorkQueue()
			now := s.Now()
			s.Now = func() time.Time { return now }
			ctx := t.Context()
			_, err := s.Create(ctx, "scheduled-runs", input("scheduled-runs", "alpha"))
			must(t, err)
			must(t, s.Enable(ctx, "scheduled-runs", true))
			now = now.Add(2 * time.Hour)
			scheduler := schedule.Service{Store: db, Now: func() time.Time { return now }}
			count, err := scheduler.Tick(ctx, 1)
			must(t, err)
			if count != 1 {
				t.Fatal("expected one logical scheduler fire")
			}
			items, err := db.WorkQueue().Lease(ctx, work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: "cleanup-schedule", Limit: 1, Now: now, Until: now.Add(time.Minute)})
			must(t, err)
			if len(items) != 1 {
				t.Fatal("scheduler occurrence was not durable")
			}
			s.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
				p, ok := kernel.FromContext(ctx)
				if !ok || p.Name != "admin" || p.TokenID != "verified-test-token" || p.Admin {
					t.Fatal("scheduler did not restore the original unprivileged reference")
				}
				return policyAuthority("*"), db.Projects().Guard(ctx)
			}
			var admitted model.CleanupRun
			handlers := map[string]schedule.Handler{cleanup.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
				if expired {
					now = now.Add(2 * time.Minute)
				}
				var err error
				admitted, err = s.ScheduledRun(ctx, trigger)
				return err
			}}
			err = scheduler.Execute(ctx, items[0], handlers)
			if expired {
				if !errors.Is(err, kernel.ErrConflict) || admitted.ID != 0 {
					t.Fatal("expired lease published scheduled work", err)
				}
				runs, _, err := db.Cleanup().Runs(ctx, port.RunFilter{}, kernel.Page{})
				must(t, err)
				if len(runs) != 0 {
					t.Fatal("lost final lease did not roll back run admission")
				}
				return
			}
			must(t, err)
			firstID := admitted.ID
			must(t, scheduler.Execute(ctx, items[0], handlers))
			if admitted.ID != firstID || admitted.Manual || admitted.DryRun {
				t.Fatal("scheduler retry replaced its logical run")
			}
			e, err := db.Cleanup().ExecutionForOccurrence(ctx, items[0].Key)
			must(t, err)
			if e.RunID != firstID {
				t.Fatal("logical fire lost its integer trigger mapping")
			}
		})
	}
}

func TestUnitCleanupLegacyEditorNameCannotExecuteUntilVerifiedEnablement(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "legacy-execution", input("legacy-execution", "alpha"))
	must(t, err)
	p.Enabled = true
	must(t, db.Cleanup().UpdatePolicy(ctx, p))
	trigger := schedule.Trigger{
		Reference: schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key},
		Revision:  p.Revision, ID: "legacy-fire", Fire: s.Now(), Fence: func(context.Context) error { return nil },
	}
	if _, err := s.ScheduledRun(ctx, trigger); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatal("a stored editor name became an executing credential", err)
	}
	must(t, s.Enable(ctx, p.Key, true))
	fresh, err := s.Get(ctx, p.Key, false)
	must(t, err)
	if fresh.Revision == p.Revision || fresh.ExecutionCredential.TokenID == "" {
		t.Fatal("verified enablement did not replace the legacy generation/grant")
	}
	stale, err := s.ScheduledRun(ctx, trigger)
	must(t, err)
	if stale.ID != 0 {
		t.Fatal("legacy queued revision survived verified re-enablement")
	}
	for _, id := range []int64{-1, 0} {
		if _, err := cleanup.PublicRunID(id); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid integer run ID converted to a public trigger", err)
		}
	}
}
