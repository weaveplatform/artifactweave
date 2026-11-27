package retention_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	fedmodel "github.com/weaveplatform/artifactweave/internal/federation/core/model"
	fedport "github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

var errExecutorCoordination = errors.New("injected executor coordination boundary fault")

type executorCoordinationFault struct {
	federationapi.CleanupCoordination
	operation          string
	hits               int
	reserved, released bool
}

func (c *executorCoordinationFault) fail(operation string, err error) error {
	if err != nil {
		return err
	}
	if c.operation == operation {
		c.hits++
		return errExecutorCoordination
	}
	return nil
}

func (c *executorCoordinationFault) Reserve(ctx context.Context, e federationapi.CleanupEvidence, run, file int64) (string, error) {
	id, err := c.CleanupCoordination.Reserve(ctx, e, run, file)
	if err == nil {
		c.reserved = true
	}
	return id, c.fail("Reserve", err)
}

func (c *executorCoordinationFault) Prepare(ctx context.Context, id string) (federationapi.CleanupReservation, error) {
	r, err := c.CleanupCoordination.Prepare(ctx, id)
	return r, c.fail("Prepare", err)
}

func (c *executorCoordinationFault) GuardReservation(ctx context.Context, id string) error {
	return c.fail("GuardReservation", c.CleanupCoordination.GuardReservation(ctx, id))
}

func (c *executorCoordinationFault) Fresh(ctx context.Context, e federationapi.CleanupEvidence) error {
	return c.fail("Fresh", c.CleanupCoordination.Fresh(ctx, e))
}

func (c *executorCoordinationFault) Commit(ctx context.Context, id string) error {
	return c.fail("Commit", c.CleanupCoordination.Commit(ctx, id))
}

func (c *executorCoordinationFault) Abort(ctx context.Context, id string) error {
	return c.fail("Abort", c.CleanupCoordination.Abort(ctx, id))
}

func (c *executorCoordinationFault) Finish(ctx context.Context, id string) error {
	err := c.CleanupCoordination.Finish(ctx, id)
	if err == nil {
		c.released = true
	}
	return c.fail("Finish", err)
}

func executorAdmission(t *testing.T, f *cleanupExecutorFixture, blocked bool) {
	t.Helper()
	exclusions := f.db.(interface {
		CleanupExclusions() artifactsapi.CleanupExclusions
	}).CleanupExclusions()
	pin, err := exclusions.Admit(f.ctx, "execution", f.ids[0])
	if blocked {
		storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
		return
	}
	storekit.Must(t, err)
	storekit.Must(t, exclusions.Complete(f.ctx, pin))
}

// Each injected coordination error follows the real successful owner operation.
// Reserve and Commit therefore prove rollback, while Prepare may durably close
// members and must retain its exact continuation until a later fenced abort.
func TestAccFederatedCleanupCoordinationFaultsRetainRecovery(t *testing.T) {
	for _, operation := range []string{"Reserve", "Prepare", "GuardReservation", "Commit", "Fresh"} {
		t.Run(operation, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			enableFederatedExecutor(t, f)
			run, item := f.selected(t)
			fault := &executorCoordinationFault{CleanupCoordination: f.policies.Federation.(federationapi.CleanupCoordination), operation: operation}
			f.executor.Policies.Federation = fault
			err := f.executor.Execute(f.ctx, item)
			storekit.WantErr(t, err, errExecutorCoordination)
			if fault.hits == 0 {
				t.Fatal("coordination fault was never reached", operation)
			}
			f.preserved(t)
			p := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			current := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
			if current.CleanedItems != 0 || current.Status.Terminal() {
				t.Fatal("failed coordination published native success", current.Status, current.CleanedItems)
			}
			if operation == "Reserve" {
				if p.CleanupIntentID != "" {
					t.Fatal("rolled-back reservation retained authority", p.CleanupIntentID)
				}
				executorAdmission(t, f, false)
				return
			}
			storekit.WantErr(t, err, work.ErrUnsettled)
			if p.CleanupIntentID == "" {
				t.Fatal("uncertainty lost durable recovery reference")
			}
			executorAdmission(t, f, true)
			fault.operation = ""
			done, err := f.executor.Step(f.ctx, run.ID, func(context.Context) error { return nil })
			storekit.Must(t, err)
			if done {
				t.Fatal("settlement prematurely completed worker")
			}
			recovered := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			if recovered.CleanupIntentID != "" {
				t.Fatal("settlement did not clear exact reference")
			}
			intent := storekit.Get[fedmodel.CleanupIntent](t)(f.db.(interface {
				CleanupIntents() fedport.CleanupIntentStore
			}).CleanupIntents().Read(f.ctx, p.CleanupIntentID))
			if intent.State != fedmodel.CleanupAborted || !intent.Released {
				t.Fatal("recovery did not preserve aborted terminal outcome", intent.State, intent.Released)
			}
			f.preserved(t)
			executorAdmission(t, f, false)
		})
	}
}

func TestAccFederatedCleanupRecoveryPublicationFaults(t *testing.T) {
	for _, operation := range []string{"Abort", "Finish", "first_progress", "second_progress", "clear_progress", "abort_fence", "clear_fence"} {
		t.Run(operation, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			enableFederatedExecutor(t, f)
			run, item := f.selected(t)
			fault := &executorCoordinationFault{CleanupCoordination: f.policies.Federation.(federationapi.CleanupCoordination), operation: "Prepare"}
			f.executor.Policies.Federation = fault
			storekit.WantErr(t, f.executor.Execute(f.ctx, item), work.ErrUnsettled)
			before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			if before.CleanupIntentID == "" {
				t.Fatal("fixture failed to prepare durable recovery")
			}
			executorAdmission(t, f, true)
			fault.operation = operation
			fault.hits = 0
			reads, fences, hits := 0, 0, 0
			wrapped := cleanupExecutionBoundaryStore{
				CleanupExecutionStore: f.store,
				cleanup: storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(ctx context.Context, method string) error {
					if !f.store.InTx(ctx) {
						return nil
					}
					fail := false
					if method == "Progress" {
						reads++
						fail = operation == "first_progress" && reads == 1 || operation == "second_progress" && reads == 2
					}
					if method == "AdvanceProgress" && operation == "clear_progress" {
						fail = true
					}
					if fail {
						hits++
						return errExecutorCoordination
					}
					return nil
				}},
			}
			f.executor.Store = wrapped
			f.executor.Policies.Store = wrapped
			fence := func(context.Context) error {
				fences++
				if operation == "abort_fence" && fences == 1 || operation == "clear_fence" && fences == 2 {
					hits++
					return errExecutorCoordination
				}
				return nil
			}
			_, err := f.executor.Step(f.ctx, run.ID, fence)
			storekit.WantErr(t, err, work.ErrUnsettled)
			storekit.WantErr(t, err, errExecutorCoordination)
			if fault.hits+hits == 0 {
				t.Fatal("recovery fault was never reached", operation, reads, fences)
			}
			retained := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			storekit.Same(t, "failed recovery publication retains exact continuation", before, retained)
			f.preserved(t)
			executorAdmission(t, f, !fault.released)
			fault.operation = ""
			f.executor.Store = f.store
			f.executor.Policies.Store = f.policies.Store
			_, err = f.executor.Step(f.ctx, run.ID, func(context.Context) error { return nil })
			storekit.Must(t, err)
			cleared := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			if cleared.CleanupIntentID != "" || cleared.Revision != before.Revision+1 {
				t.Fatal("recovery did not clear exactly once", cleared.Revision, before.Revision)
			}
			f.preserved(t)
			executorAdmission(t, f, false)
		})
	}
}

// Losing a required coordinator during recovery cannot turn an unknown prepared
// operation into a completed run, even when the queue is on its final attempt.
func TestAccFederatedCleanupMissingRecoveryOwnerStaysUnsettled(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	enableFederatedExecutor(t, f)
	run, item := f.selected(t)
	fault := &executorCoordinationFault{CleanupCoordination: f.policies.Federation.(federationapi.CleanupCoordination), operation: "Prepare"}
	f.executor.Policies.Federation = fault
	storekit.WantErr(t, f.executor.Execute(f.ctx, item), work.ErrUnsettled)
	before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
	f.executor.Policies.Federation = struct{ federationapi.Cleanup }{fault.CleanupCoordination}
	item.FinalAttempt = true
	storekit.WantErr(t, f.executor.Execute(f.ctx, item), work.ErrUnsettled)
	storekit.Same(t, "missing owner keeps exact recovery reference", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
	f.preserved(t)
	executorAdmission(t, f, true)
}
