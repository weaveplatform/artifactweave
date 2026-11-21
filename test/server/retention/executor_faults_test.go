package retention_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	retport "github.com/weaveplatform/artifactweave/internal/retention/core/port"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type cleanupExecutionBoundaryStore struct {
	retport.CleanupExecutionStore
	cleanup retport.CleanupStore
}

func (s cleanupExecutionBoundaryStore) Cleanup() retport.CleanupStore { return s.cleanup }

type cleanupCorruptAcknowledgement struct {
	artifactsapi.ItemRemoval
	corrupt func(*artifactsapi.RemovalResult)
}

func (r cleanupCorruptAcknowledgement) Remove(
	ctx context.Context,
	request artifactsapi.RemovalRequest,
	validate func(context.Context) error,
	record func(context.Context, artifactsapi.RemovalResult) error,
) (artifactsapi.RemovalResult, error) {
	return r.ItemRemoval.Remove(ctx, request, validate, func(ctx context.Context, result artifactsapi.RemovalResult) error {
		r.corrupt(&result)
		return record(ctx, result)
	})
}

func TestAccCleanupExecutorFaultsReference(t *testing.T) {
	cleanupExecutorBoundaries(t, func(*testing.T) harness.Store { return inmem.New() })
}

func TestAccCleanupExecutorFaultsSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			cleanupExecutorBoundaries(t, func(t *testing.T) harness.Store { return newStore(t) })
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func cleanupExecutorBoundaries(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	cleanupDiscoveryBoundaries(t, newStore)
	cleanupFailureBoundaries(t, newStore)
	for _, tc := range []struct {
		name         string
		operation    string
		inTx         bool
		afterRemoval bool
	}{
		{name: "execution read", operation: "Execution"},
		{name: "continuation read", operation: "Progress"},
		{name: "run read", operation: "Run"},
		{name: "current policy", operation: "Policy", inTx: true},
		{name: "current run", operation: "Run", inTx: true},
		{name: "next intent", operation: "PendingEntry", inTx: true},
		{name: "ack continuation lock", operation: "Progress", inTx: true, afterRemoval: true},
		{name: "ack run lock", operation: "Run", inTx: true, afterRemoval: true},
		{name: "ack entry read", operation: "Entry", inTx: true, afterRemoval: true},
		{name: "ack entry write", operation: "AdvanceEntry", inTx: true, afterRemoval: true},
		{name: "ack continuation write", operation: "AdvanceProgress", inTx: true, afterRemoval: true},
		{name: "ack run write", operation: "UpdateRun", inTx: true, afterRemoval: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, newStore(t))
			r, w := f.selected(t)
			beforeRun := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			beforeProgress := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
			usage := f.db.Repos().(artport.StorageUsage)
			beforeBytes := storekit.Get[int64](t)(usage.RepositoryStorageBytes(f.ctx, "execution"))
			origin := storekit.Get[string](t)(f.db.Instance().ID(f.ctx))
			_, err := f.db.Events().RegisterConsumer(f.ctx, origin, "fault-proof")
			storekit.Must(t, err)
			beforeEvents, _, err := f.db.Events().Next(f.ctx, origin, "fault-proof", 100)
			storekit.Must(t, err)
			removed, hit := false, false
			faulty := f.executor
			faulty.Removal = cleanupBeforeAcknowledgement{ItemRemoval: faulty.Removal, before: func() { removed = true }}
			faultStore := cleanupExecutionBoundaryStore{
				CleanupExecutionStore: f.store,
				cleanup: storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(ctx context.Context, method string) error {
					if method == tc.operation && f.store.InTx(ctx) == tc.inTx && (!tc.afterRemoval || removed) {
						hit = true
						return errCleanupEffectAck
					}
					return nil
				}},
			}
			faulty.Store, faulty.Policies.Store = faultStore, faultStore
			err = faulty.Execute(f.ctx, w)
			storekit.WantErr(t, err, errCleanupEffectAck)
			if !hit {
				t.Fatal("fault boundary was not exercised")
			}
			f.preserved(t)
			storekit.Same(t, "failed effect leaves run counters", beforeRun, storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID)))
			storekit.Same(t, "failed effect leaves continuation", beforeProgress, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID)))
			storekit.Same(t, "failed effect leaves usage", beforeBytes, storekit.Get[int64](t)(usage.RepositoryStorageBytes(f.ctx, "execution")))
			afterEvents, _, err := f.db.Events().Next(f.ctx, origin, "fault-proof", 100)
			storekit.Must(t, err)
			storekit.Same(t, "failed effect leaves event stream", beforeEvents, afterEvents)
			storekit.Must(t, f.executor.Execute(f.ctx, w))
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if finished.Status != retapi.RunCompleted || finished.TotalItems != 2 || finished.CleanedItems != 2 {
				t.Fatal("recovery did not acknowledge exactly the committed files", finished)
			}
			storekit.Must(t, f.executor.Execute(f.ctx, w))
			storekit.Same(t, "terminal replay leaves counters", finished, storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID)))
		})
	}
	for _, tc := range []struct {
		name    string
		corrupt func(*artifactsapi.RemovalResult)
	}{
		{name: "zero files", corrupt: func(r *artifactsapi.RemovalResult) { r.Files = 0 }},
		{name: "file count mismatch", corrupt: func(r *artifactsapi.RemovalResult) { r.Files++ }},
		{name: "byte count mismatch", corrupt: func(r *artifactsapi.RemovalResult) { r.Bytes++ }},
		{name: "negative item size", corrupt: func(r *artifactsapi.RemovalResult) { r.Items[0].Size = -1 }},
		{name: "overflowing item sizes", corrupt: func(r *artifactsapi.RemovalResult) {
			r.Items = append(r.Items, r.Items[0])
			r.Items[0].Size, r.Items[1].Size = math.MaxInt64, 1
			r.Files = 2
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, newStore(t))
			r, w := f.selected(t)
			before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
			faulty := f.executor
			faulty.Removal = cleanupCorruptAcknowledgement{ItemRemoval: f.executor.Removal, corrupt: tc.corrupt}
			if err := faulty.Execute(f.ctx, w); err == nil || !errors.Is(err, kernel.ErrStore) {
				t.Fatal("inconsistent owner acknowledgement was accepted", err)
			}
			f.preserved(t)
			storekit.Same(t, "rejected owner result leaves continuation", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID)))
			storekit.Must(t, f.executor.Execute(f.ctx, w))
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if finished.Status != retapi.RunCompleted || finished.CleanedItems != 2 || finished.CleanedSize != int64(len("real-one.bin")+len("real-two.bin")) {
				t.Fatal("recovery counted inconsistent or duplicate effects", finished)
			}
		})
	}
}

func cleanupDiscoveryBoundaries(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	for _, phase := range []model.CleanupPhase{model.CleanupRepositories, model.CleanupPackages, model.CleanupVersions, model.CleanupCandidates} {
		for _, operation := range []string{"AdvanceProgress", "UpdateRun", "Policy", "CreateEntry"} {
			if operation == "CreateEntry" && phase != model.CleanupCandidates {
				continue
			}
			t.Run("discovery/"+string(phase)+"/"+operation, func(t *testing.T) {
				f := newCleanupExecutorFixture(t, newStore(t))
				r, err := f.policies.ManualRun(f.caller, "execution-policy", false)
				storekit.Must(t, err)
				w, taken, err := f.db.WorkQueue().
					LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10), work.WorkLease{Holder: "discovery-fault", Now: f.now, Until: f.now.Add(time.Hour)})
				storekit.Must(t, err)
				if !taken {
					t.Fatal("cleanup work was not available")
				}
				fence := func(ctx context.Context) error {
					_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
					return err
				}
				for range 100 {
					p := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
					run := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
					if p.Phase == phase && !run.Started.IsZero() {
						break
					}
					_, err := f.executor.Step(f.ctx, r.ID, fence)
					storekit.Must(t, err)
				}
				beforeRun := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
				before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
				if before.Phase != phase {
					t.Fatal("fixture missed its discovery page")
				}
				beforeEntries, _, err := f.db.Cleanup().Entries(f.ctx, retport.CleanupEntryQuery{RunID: r.ID}, kernel.Page{Limit: 100})
				storekit.Must(t, err)
				hit := false
				faulty := f.executor
				faultStore := cleanupExecutionBoundaryStore{
					CleanupExecutionStore: f.store,
					cleanup: storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, method string) error {
						if method == operation {
							hit = true
							return errCleanupEffectAck
						}
						return nil
					}},
				}
				faulty.Store, faulty.Policies.Store = faultStore, faultStore
				_, err = faulty.Step(f.ctx, r.ID, fence)
				storekit.WantErr(t, err, errCleanupEffectAck)
				if !hit {
					t.Fatal("discovery fault was not exercised")
				}
				storekit.Same(t, "discovery failure preserves continuation", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID)))
				storekit.Same(t, "discovery failure preserves run", beforeRun, storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID)))
				afterEntries, _, err := f.db.Cleanup().Entries(f.ctx, retport.CleanupEntryQuery{RunID: r.ID}, kernel.Page{Limit: 100})
				storekit.Must(t, err)
				storekit.Same(t, "discovery failure preserves intent set", beforeEntries, afterEntries)
				f.preserved(t)
				storekit.Must(t, f.executor.Execute(f.ctx, w))
				finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
				if finished.Status != retapi.RunCompleted || finished.TotalItems != 2 || finished.CleanedItems != 2 {
					t.Fatal("discovery retry lost or duplicated candidates", finished)
				}
			})
		}
	}
}

func cleanupFailureBoundaries(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	for _, operation := range []string{"Execution", "Progress", "Run", "PendingEntry", "Entry", "AdvanceEntry", "AdvanceProgress", "UpdateRun"} {
		t.Run("retry exhaustion/"+operation, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, newStore(t))
			r, w := f.selected(t)
			beforeRun := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
			failed, hit := false, false
			faulty := f.executor
			faulty.Removal = cleanupPersistentItemFault{ItemRemoval: faulty.Removal, id: f.ids[0], beforeFailure: func() { failed = true }}
			faulty.Store = cleanupExecutionBoundaryStore{
				CleanupExecutionStore: f.store,
				cleanup: storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, method string) error {
					if failed && method == operation {
						hit = true
						return errCleanupEffectAck
					}
					return nil
				}},
			}
			w.FinalAttempt = true
			err := faulty.Execute(f.ctx, w)
			storekit.WantErr(t, err, work.ErrUnsettled)
			storekit.WantErr(t, err, errCleanupEffectAck)
			if !hit {
				t.Fatal("retry exhaustion fault was not exercised")
			}
			f.preserved(t)
			storekit.Same(t, "unrecorded exhaustion preserves run", beforeRun, storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID)))
			storekit.Same(t, "unrecorded exhaustion preserves continuation", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID)))
			// A restarted final attempt can now record the genuine failure. It
			// must not delete the remaining files or claim a successful run.
			recovered := f.executor
			recovered.Removal = cleanupPersistentItemFault{ItemRemoval: recovered.Removal, id: f.ids[0]}
			storekit.Must(t, recovered.Execute(f.ctx, w))
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if finished.Status != retapi.RunFailed || finished.CleanedItems != 0 || finished.CleanedSize != 0 {
				t.Fatal("exhaustion recovery invented successful effects", finished)
			}
			f.preserved(t)
		})
	}
}
