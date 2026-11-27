package retention_test

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	retport "github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

type cleanupInvalidExecutionStore struct{ retport.CleanupStore }

func (s cleanupInvalidExecutionStore) Execution(ctx context.Context, id int64) (model.CleanupExecution, error) {
	e, err := s.CleanupStore.Execution(ctx, id)
	e.Credential = kernel.CredentialReference{}
	return e, err
}

type cleanupLeaseWaitQueue struct {
	work.WorkQueue
	after func(time.Time)
}

func (s cleanupLeaseWaitQueue) GuardLease(ctx context.Context, kind, key string, generation int64) (time.Time, error) {
	until, err := s.WorkQueue.GuardLease(ctx, kind, key, generation)
	if err == nil {
		s.after(until)
	}
	return until, err
}

type cleanupLeaseWaitStore struct {
	retport.CleanupExecutionStore
	queue work.WorkQueue
}

func (s cleanupLeaseWaitStore) WorkQueue() work.WorkQueue { return s.queue }

func TestAccCleanupWorkerRefusesMissingOrMalformedAuthority(t *testing.T) {
	for _, mode := range []string{"store", "guard", "restore", "removal", "fence", "transaction", "execution", "work_kind", "work_reference", "noncanonical_reference"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			run, item := f.selected(t)
			before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			worker := f.executor
			fence := func(context.Context) error { return nil }
			var expected error = kernel.ErrStore
			switch mode {
			case "store":
				worker.Store = nil
			case "guard":
				worker.Policies.Guard = nil
			case "restore":
				worker.Policies.Restore = nil
			case "removal":
				worker.Removal = nil
			case "fence":
				fence = nil
			case "execution":
				worker.Store = cleanupExecutionBoundaryStore{f.store, cleanupInvalidExecutionStore{f.db.Cleanup()}}
				expected = kernel.ErrBadCredentials
			}
			var err error
			switch mode {
			case "work_kind":
				item.Kind = "foreign-work"
				err = worker.Execute(f.ctx, item)
				expected = work.ErrPermanent
			case "work_reference":
				item.Key = "0"
				err = worker.Execute(f.ctx, item)
				expected = work.ErrPermanent
			case "noncanonical_reference":
				item.Key = "0" + item.Key
				err = worker.Execute(f.ctx, item)
				expected = work.ErrPermanent
			case "transaction":
				err = f.store.Tx(f.ctx, func(ctx context.Context) error { _, err := worker.Step(ctx, run.ID, fence); return err })
				expected = kernel.ErrInvalid
			default:
				_, err = worker.Step(f.ctx, run.ID, fence)
			}
			storekit.WantErr(t, err, expected)
			f.preserved(t)
			storekit.Same(t, "refused worker has no continuation authority", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
		})
	}
}

func TestAccCleanupWorkerPublicationWaitRechecksTime(t *testing.T) {
	for _, mode := range []string{"duration", "lease"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			run, item := f.selected(t)
			if mode == "duration" {
				advanced := false
				done, err := f.executor.Step(f.ctx, run.ID, func(context.Context) error {
					if !advanced {
						f.now = f.now.Add(2 * time.Minute)
						advanced = true
					}
					return nil
				})
				storekit.Must(t, err)
				if !done || !advanced {
					t.Fatal("late duration limit did not terminate current owner")
				}
				stopped := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
				p := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
				if stopped.Status != retapi.RunStopped || stopped.CleanedItems != 0 || p.Reason != "duration_limit" {
					t.Fatal("late duration check published a native effect", stopped.Status, stopped.CleanedItems, p.Reason)
				}
			} else {
				before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
				hit := false
				worker := f.executor
				worker.Store = cleanupLeaseWaitStore{f.store, cleanupLeaseWaitQueue{f.db.WorkQueue(), func(until time.Time) { f.now = until; hit = true }}}
				storekit.WantErr(t, worker.Execute(f.ctx, item), kernel.ErrConflict)
				if !hit {
					t.Fatal("late lease clock check was not reached")
				}
				storekit.Same(t, "expired lease cannot acknowledge effects", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
			}
			f.preserved(t)
		})
	}
}

func TestAccCleanupWorkerCommittedEffectSurvivesLostResultRead(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	run, item := f.selected(t)
	reads := 0
	worker := f.executor
	worker.Store = cleanupExecutionBoundaryStore{f.store, storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(ctx context.Context, operation string) error {
		if operation == "Run" && !f.store.InTx(ctx) {
			reads++
			if reads == 2 {
				return errExecutorCoordination
			}
		}
		return nil
	}}}
	_, err := worker.Step(f.ctx, run.ID, func(context.Context) error { return nil })
	storekit.WantErr(t, err, errExecutorCoordination)
	committed := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
	if reads != 2 || committed.CleanedItems != 1 {
		t.Fatal("lost read falsely rolled back a committed acknowledgement", reads, committed.CleanedItems)
	}
	storekit.Must(t, f.executor.Execute(f.ctx, item))
	finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
	if finished.Status != retapi.RunCompleted || finished.CleanedItems != 2 {
		t.Fatal("retry double counted/lost committed file", finished.Status, finished.CleanedItems)
	}
}

func TestAccCleanupFinalFailureSettlementCanStopOrRecover(t *testing.T) {
	for _, mode := range []string{"guard", "entry_write", "stop"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			run, item := f.selected(t)
			item.FinalAttempt = true
			worker := f.executor
			worker.Removal = cleanupPersistentItemFault{ItemRemoval: worker.Removal, id: f.ids[0]}
			worker.Store = cleanupExecutionBoundaryStore{f.store, storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(ctx context.Context, operation string) error {
				if operation == "Entries" {
					return errExecutorCoordination
				}
				return nil
			}}}
			storekit.WantErr(t, worker.Execute(f.ctx, item), work.ErrUnsettled)
			before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			if before.Phase != model.CleanupFailing {
				t.Fatal("fixture never reached real retry exhaustion", before.Phase)
			}
			worker = f.executor
			hit := false
			switch mode {
			case "guard":
				worker.Policies.Guard = func(context.Context) error { hit = true; return errExecutorCoordination }
			case "entry_write":
				worker.Store = cleanupExecutionBoundaryStore{f.store, storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, operation string) error {
					if operation == "AdvanceEntry" {
						hit = true
						return errExecutorCoordination
					}
					return nil
				}}}
			case "stop":
				storekit.Must(t, f.policies.Stop(f.caller, run.ID))
			}
			_, err := worker.Step(f.ctx, run.ID, func(context.Context) error { return nil })
			if mode == "stop" {
				storekit.Must(t, err)
				stopped := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
				if stopped.Status != retapi.RunStopped {
					t.Fatal("stop during failure settlement was lost", stopped.Status)
				}
			} else {
				storekit.WantErr(t, err, errExecutorCoordination)
				if !hit {
					t.Fatal("failure settlement boundary was not reached")
				}
				storekit.Same(t, "failure publication rolls back continuation", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
				storekit.Must(t, f.executor.Execute(f.ctx, item))
				finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
				if finished.Status != retapi.RunFailed || finished.CleanedItems != 0 {
					t.Fatal("failure recovery invented successful removal", finished.Status, finished.CleanedItems)
				}
			}
			f.preserved(t)
		})
	}
}
