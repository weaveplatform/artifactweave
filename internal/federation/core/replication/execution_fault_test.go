package replication_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errExecutionStorage = errors.Join(kernel.ErrStore, errors.New("private execution storage diagnostic"))

type faultDispatchStore struct {
	replication.CoordinatorStore
	progress port.ExecutionProgressStore
	queue    work.WorkQueue
}

func (s faultDispatchStore) Executions() port.ExecutionProgressStore { return s.progress }
func (s faultDispatchStore) WorkQueue() work.WorkQueue               { return s.queue }

type faultExecutionProgress struct {
	port.ExecutionProgressStore
	inTx                func(context.Context) bool
	created             model.ExecutionRun
	createError         bool
	optionsError        bool
	optionsReadError    bool
	borrowedOptions     bool
	publicationError    bool
	afterInitialRead    bool
	reads               int
	runError            bool
	lockedReadPhase     model.ExecutionPhase
	advanceFailurePhase model.ExecutionPhase
	effectError         bool
}

func (p *faultExecutionProgress) Create(ctx context.Context, id string, binding model.ExecutionBinding, at time.Time) (model.ExecutionRun, error) {
	run, err := p.ExecutionProgressStore.Create(ctx, id, binding, at)
	p.created = run
	if err == nil && p.createError {
		return run, errExecutionStorage
	}
	return run, err
}

func (p *faultExecutionProgress) PutOptions(ctx context.Context, ref model.ExecutionReference, options model.ExecutionOptions) error {
	if err := p.ExecutionProgressStore.PutOptions(ctx, ref, options); err != nil {
		return err
	}
	if p.optionsError {
		return errExecutionStorage
	}
	return nil
}

func (p *faultExecutionProgress) Options(ctx context.Context, ref model.ExecutionReference) (model.ExecutionOptions, error) {
	if p.optionsReadError {
		return model.ExecutionOptions{}, errExecutionStorage
	}
	options, err := p.ExecutionProgressStore.Options(ctx, ref)
	if err == nil && p.borrowedOptions {
		options.Independent, options.RepositoryKey = true, "foreign-repository"
		options.TargetKey, options.TargetRevision = "foreign-target", "foreign-revision"
	}
	return options, err
}

func (p *faultExecutionProgress) Run(ctx context.Context, ref model.ExecutionReference) (model.ExecutionRun, error) {
	p.reads++
	if p.afterInitialRead && p.reads > 1 {
		return model.ExecutionRun{}, errExecutionStorage
	}
	if p.runError {
		return model.ExecutionRun{}, errExecutionStorage
	}
	run, err := p.ExecutionProgressStore.Run(ctx, ref)
	if err == nil && run.Phase == p.lockedReadPhase && p.inTx != nil && p.inTx(ctx) {
		return model.ExecutionRun{}, errExecutionStorage
	}
	return run, err
}

func TestUnitReplicationWorkerDispatchReadFailureAfterClaimKeepsOriginalRunRetryable(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPush)
	progress := &faultExecutionProgress{ExecutionProgressStore: w.db.Executions(), afterInitialRead: true}
	w.worker.Store = faultDispatchStore{CoordinatorStore: w.db, progress: progress, queue: w.db.WorkQueue()}
	if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, errExecutionStorage) || progress.reads != 2 {
		t.Fatal("post-claim dispatch read did not fail at the intended boundary", err)
	}
	current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if !reflect.DeepEqual(current, w.run) || w.push.puts != 0 {
		t.Fatal("post-claim read failure retained started progress or artifact effects")
	}
	progress.afterInitialRead = false
	must(t, w.worker.Execute(t.Context(), w.item))
	current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
		t.Fatal("same-run retry after post-claim read failure lost or repeated effects")
	}
}

func TestUnitReplicationWorkerRechecksCurrentRepositoryBeforeStartingDirection(t *testing.T) {
	for _, stage := range []string{"authorization revoked", "direction changed", "driver unavailable", "transactional caller"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPush)
			configuration := w.push.repos.values["local"]
			policy := w.push.policy
			step := w.worker.Drivers[model.ReplicationPush]
			var want error = kernel.ErrConflict
			switch stage {
			case "authorization revoked":
				w.push.policy = authority{any: true, project: "another-project"}
				want = kernel.ErrForbidden
			case "direction changed":
				changed := configuration
				changed.Class = repo.Remote
				w.push.repos.values["local"] = changed
			case "driver unavailable":
				delete(w.worker.Drivers, model.ReplicationPush)
				want = kernel.ErrStore
			case "transactional caller":
				want = kernel.ErrStore
			}
			invoke := func(ctx context.Context) error { return w.worker.Execute(ctx, w.item) }
			var err error
			if stage == "transactional caller" {
				err = w.db.Tx(t.Context(), invoke)
			} else {
				err = invoke(t.Context())
			}
			if !errors.Is(err, want) {
				t.Fatal("dispatch retained stale repository authority or unavailable direction", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if !reflect.DeepEqual(current, w.run) || w.push.puts != 0 {
				t.Fatal("refused current repository changed dispatch or peer bytes")
			}
			w.push.repos.values["local"], w.push.policy = configuration, policy
			w.worker.Drivers[model.ReplicationPush] = step
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Status() != model.ReplicationOK || current.Outcomes.Succeeded != 1 {
				t.Fatal("restored current authority failed to recover original dispatch")
			}
		})
	}
}

func (p *faultExecutionProgress) Advance(ctx context.Context, expected int64, next model.ExecutionRun) error {
	if p.publicationError {
		if err := tx.SetPublicationCheck(ctx, func(context.Context) error { return errExecutionStorage }); err != nil {
			return err
		}
	}
	if next.Phase == p.advanceFailurePhase {
		return errExecutionStorage
	}
	return p.ExecutionProgressStore.Advance(ctx, expected, next)
}

func TestUnitReplicationPublicationSettlementFailureRollsBackDispatchStart(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPush)
	progress := &faultExecutionProgress{ExecutionProgressStore: w.db.Executions(), publicationError: true}
	w.worker.Store = faultDispatchStore{CoordinatorStore: w.db, progress: progress, queue: w.db.WorkQueue()}
	if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, errExecutionStorage) {
		t.Fatal("failed ordinary-work settlement published execution progress", err)
	}
	current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if !reflect.DeepEqual(current, w.run) || w.push.puts != 0 {
		t.Fatal("failed transaction settlement retained a started dispatch or peer effect")
	}
	progress.publicationError = false
	must(t, w.worker.Execute(t.Context(), w.item))
	current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
		t.Fatal("healthy settlement did not recover the original execution exactly once")
	}
}

func (p *faultExecutionProgress) RecordEffect(ctx context.Context, effect model.ExecutionEffect) error {
	if err := p.ExecutionProgressStore.RecordEffect(ctx, effect); err != nil {
		return err
	}
	if p.effectError {
		return errExecutionStorage
	}
	return nil
}

type faultDispatchQueue struct {
	work.WorkQueue
	readError, enqueueError bool
}

func (q faultDispatchQueue) Get(ctx context.Context, kind, key string) (work.WorkItem, error) {
	if q.readError {
		return work.WorkItem{}, errExecutionStorage
	}
	return q.WorkQueue.Get(ctx, kind, key)
}

func (q faultDispatchQueue) Enqueue(ctx context.Context, kind, key string, payload []byte, at time.Time) error {
	if err := q.WorkQueue.Enqueue(ctx, kind, key, payload, at); err != nil {
		return err
	}
	if q.enqueueError {
		return errExecutionStorage
	}
	return nil
}

func TestUnitReplicationAdmissionRollsBackEveryComponentAfterStorageFailure(t *testing.T) {
	for _, stage := range []string{"execution", "options", "queue read", "queue write"} {
		t.Run(stage, func(t *testing.T) {
			_, db, coordinator, ctx := coordinatorFixture(t, "local", false)
			progress := &faultExecutionProgress{ExecutionProgressStore: db.Executions(), inTx: db.InTx}
			queue := faultDispatchQueue{WorkQueue: db.WorkQueue()}
			switch stage {
			case "execution":
				progress.createError = true
			case "options":
				progress.optionsError = true
			case "queue read":
				queue.readError = true
			case "queue write":
				queue.enqueueError = true
			}
			coordinator.Store = faultDispatchStore{CoordinatorStore: db, progress: progress, queue: queue}
			returned, err := coordinator.ExecuteWithOptions(ctx, "local", "selected", replication.ExecutionInput{Include: ptr("**/*.bin")})
			if !errors.Is(err, errExecutionStorage) || returned.Reference.Validate() == nil || progress.created.Reference.Validate() != nil {
				t.Fatal("storage refusal returned usable admission or missed the intended write", err)
			}
			if _, err := db.Executions().Run(ctx, progress.created.Reference); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("failed compound admission retained its logical run", err)
			}
			if _, err := db.Executions().Options(ctx, progress.created.Reference); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("failed compound admission retained private options", err)
			}
			var admitted bool
			if err := db.platform.Settings().Get(ctx, "admitted-transfer:"+progress.created.Reference.ID, &admitted); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("direction progress escaped the rejected admission transaction", err)
			}
			stats, err := db.WorkQueue().Stats(ctx)
			must(t, err)
			if len(stats) != 0 {
				t.Fatal("failed compound admission retained queued work")
			}
		})
	}
}

func TestUnitReplicationScheduledReplayCannotReplaceAnotherQueuedReference(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`[]`), []byte(`{"id":"another-run","incarnation":"another-lifetime"}`)} {
		w, db, coordinator, ctx := coordinatorFixture(t, "local", true)
		target := stored(t, w, "local")
		trigger := schedule.Trigger{
			Reference: schedule.Reference{Kind: replication.ScheduleKind, Key: target.Key}, Revision: target.Revision,
			Fire: time.Unix(1, 0), ID: "immutable-fire", Fence: func(context.Context) error { return nil },
		}
		run, err := coordinator.Scheduled(t.Context(), trigger)
		must(t, err)
		item := leaseExecution(t, db)
		must(t, db.WorkQueue().Release(ctx, item.Kind, item.Key, item.Generation, time.Unix(100, 0)))
		must(t, db.WorkQueue().Enqueue(ctx, item.Kind, item.Key, payload, time.Unix(100, 0)))
		before, err := db.WorkQueue().Get(ctx, item.Kind, item.Key)
		must(t, err)
		if _, err := coordinator.Scheduled(t.Context(), trigger); !errors.Is(err, kernel.ErrStore) {
			t.Fatal("scheduled replay overwrote a different private queue identity", err)
		}
		current, err := db.Executions().Run(ctx, run.Reference)
		must(t, err)
		after, err := db.WorkQueue().Get(ctx, item.Kind, item.Key)
		must(t, err)
		if !reflect.DeepEqual(current, run) || !reflect.DeepEqual(before, after) {
			t.Fatal("rejected replay changed retained execution or conflicting queue evidence")
		}
	}
}

func TestUnitReplicationWorkerRejectsMalformedWorkBeforeRestoringAuthority(t *testing.T) {
	for _, mode := range []string{"wrong kind", "wrong key", "unknown field", "trailing object"} {
		t.Run(mode, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPush)
			item := w.item
			switch mode {
			case "wrong kind":
				item.Kind = "unrelated:work"
			case "wrong key":
				item.Key = "another-execution"
			case "unknown field":
				var err error
				item.Payload, err = json.Marshal(map[string]string{"id": w.run.Reference.ID, "incarnation": w.run.Reference.Incarnation, "extra": "must-refuse"})
				must(t, err)
			case "trailing object":
				item.Payload = append(append([]byte(nil), item.Payload...), []byte(` {}`)...)
			}
			restored := false
			w.worker.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) {
				restored = true
				return nil, kernel.ErrBadCredentials
			}
			if err := w.worker.Execute(t.Context(), item); !errors.Is(err, work.ErrPermanent) || restored || w.push.puts != 0 {
				t.Fatal("malformed queue reference reached author restoration or peer effects", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if !reflect.DeepEqual(current, w.run) {
				t.Fatal("malformed queue reference mutated another logical execution")
			}
		})
	}
}

func TestUnitReplicationWorkerRejectsDifferentOrTransactionalRestoredCaller(t *testing.T) {
	for _, mode := range []string{"denied", "nil", "anonymous", "different token", "transaction"} {
		t.Run(mode, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPush)
			var restored context.Context
			if mode == "transaction" {
				must(t, w.db.Tx(t.Context(), func(ctx context.Context) error { restored = ctx; return nil }))
			}
			w.worker.Restore = func(ctx context.Context, ref kernel.CredentialReference) (context.Context, error) {
				if w.db.InTx(ctx) || ref != w.run.Binding.Credential {
					t.Fatal("worker restored authority inside metadata transaction or for a different credential")
				}
				switch mode {
				case "denied":
					return nil, kernel.ErrBadCredentials
				case "nil":
					return nil, nil
				case "anonymous":
					return ctx, nil
				case "different token":
					return accessapi.WithCaller(ctx, kernel.Principal{Name: ref.Name, TokenID: "other-token"}, nil), nil
				default:
					return restored, nil
				}
			}
			var want error = kernel.ErrBadCredentials
			if mode == "nil" || mode == "transaction" {
				want = kernel.ErrStore
			}
			if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, want) || w.push.puts != 0 {
				t.Fatal("invalid restored authority reached peer effects", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if !reflect.DeepEqual(current, w.run) {
				t.Fatal("refused restoration published execution progress")
			}
		})
	}
}

func TestUnitReplicationFinalRefusalsRetainClosedTruthfulFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want model.ExecutionFailure
	}{
		{"authority", kernel.ErrForbidden, model.ExecutionAuthorityDenied},
		{"configuration", kernel.ErrConflict, model.ExecutionConfigurationChanged},
		{"offline", artifactsapi.ErrPullOffline, model.ExecutionSourceOffline},
		{"no storage", artifactsapi.ErrPullNotStored, model.ExecutionNotStored},
		{"integrity", artifactsapi.ErrPullIntegrity, model.ExecutionIntegrityFailed},
		{"unsupported", artifactsapi.ErrPullUnsupported, model.ExecutionUnsupported},
		{"transient exhausted", errExecutionStorage, model.ExecutionRetryExhausted},
		{"selection limit", replication.ErrSelectionLimit, model.ExecutionSelectionLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPull)
			w.item.FinalAttempt = tc.want != model.ExecutionSelectionLimited
			w.worker.Drivers[model.ReplicationPull] = func(ctx context.Context, _ model.ExecutionRun, _ replication.PushFence) error {
				if w.db.InTx(ctx) {
					t.Fatal("direction work borrowed a metadata transaction")
				}
				return errors.Join(tc.err, errors.New("private transfer diagnostic"))
			}
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Phase != model.ExecutionAborted || current.Failure != tc.want || current.Status() != model.ReplicationError ||
				current.Outcomes != (model.ExecutionOutcomes{}) || current.Completed.IsZero() || w.pull.fetches != 0 {
				t.Fatal("final refusal fabricated effects or lost its closed truthful failure", current.Phase, current.Failure, current.Outcomes)
			}
			w.worker.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) {
				t.Fatal("terminal replay restored authority")
				return nil, nil
			}
			must(t, w.worker.Execute(t.Context(), w.item))
		})
	}
}

func TestUnitReplicationSettlementStoreFailureRemainsRetryable(t *testing.T) {
	for _, stage := range []string{"initial read", "locked admission read", "start write", "abort read", "abort write"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPush)
			progress := &faultExecutionProgress{ExecutionProgressStore: w.db.Executions(), inTx: w.db.InTx}
			switch stage {
			case "initial read":
				progress.runError = true
			case "locked admission read":
				progress.lockedReadPhase = model.ExecutionPending
			case "start write":
				progress.advanceFailurePhase = model.ExecutionRunning
			case "abort read":
				progress.lockedReadPhase = model.ExecutionRunning
			case "abort write":
				progress.advanceFailurePhase = model.ExecutionAborted
			}
			w.worker.Store = faultDispatchStore{CoordinatorStore: w.db, progress: progress, queue: w.db.WorkQueue()}
			originalStep := w.worker.Drivers[model.ReplicationPush]
			if strings.HasPrefix(stage, "abort") {
				w.item.FinalAttempt = true
				w.worker.Drivers[model.ReplicationPush] = func(context.Context, model.ExecutionRun, replication.PushFence) error { return errExecutionStorage }
			}
			err := w.worker.Execute(t.Context(), w.item)
			if !errors.Is(err, errExecutionStorage) || strings.Contains(err.Error(), "private execution storage") || w.push.puts != 0 {
				t.Fatal("storage refusal lost retry identity, disclosed diagnostics, or committed a peer effect", err)
			}
			if strings.HasPrefix(stage, "abort") && !errors.Is(err, work.ErrUnsettled) {
				t.Fatal("uncommitted failure settlement could be parked as finished", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Phase == model.ExecutionComplete || current.Phase == model.ExecutionAborted || current.Failure != "" || current.Outcomes != (model.ExecutionOutcomes{}) {
				t.Fatal("failed persistence manufactured a terminal result")
			}
			w.worker.Store = w.db
			w.worker.Drivers[model.ReplicationPush] = originalStep
			w.item.FinalAttempt = false
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) || w.push.targetBodies["selected/file"] != "verified bytes" {
				t.Fatal("resuming the retained logical run lost or duplicated the actual effect")
			}
		})
	}
}

func TestUnitReplicationExpiredFinalAttemptCannotParkUnfinishedTransfer(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPush)
	w.item.FinalAttempt = true
	w.worker.Now = func() time.Time { return time.Unix(200, 0) }
	if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrConflict) || !errors.Is(err, work.ErrUnsettled) || w.push.puts != 0 {
		t.Fatal("expired final attempt performed or settled unowned work", err)
	}
	current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if !reflect.DeepEqual(current, w.run) {
		t.Fatal("expired attempt mutated retained execution evidence")
	}
	items, err := w.db.WorkQueue().
		Lease(t.Context(), work.WorkLease{Kinds: []string{replication.ExecutionWorkKind}, Holder: "successor", Limit: 1, Now: time.Unix(201, 0), Until: time.Unix(301, 0)})
	must(t, err)
	if len(items) != 1 || items[0].Generation <= w.item.Generation {
		t.Fatal("expired attempt stranded its unfinished queue reference")
	}
	w.worker.Now = func() time.Time { return time.Unix(201, 0) }
	must(t, w.worker.Execute(t.Context(), items[0]))
	current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Status() != model.ReplicationOK || current.Outcomes.Succeeded != 1 {
		t.Fatal("successor did not resume the same execution exactly once")
	}
}
