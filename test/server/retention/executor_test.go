package retention_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	retport "github.com/weaveplatform/artifactweave/internal/retention/core/port"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

var errCleanupEffectAck = errors.New("injected cleanup fault after acknowledgement")

type cleanupAckFault struct {
	retport.CleanupStore
	armed *bool
}

func (c cleanupAckFault) AdvanceEntry(ctx context.Context, revision int64, e model.CleanupEntry) error {
	if err := c.CleanupStore.AdvanceEntry(ctx, revision, e); err != nil {
		return err
	}
	if *c.armed && e.State == model.CleanupDeleted {
		*c.armed = false
		return errCleanupEffectAck
	}
	return nil
}

type cleanupFaultStore struct {
	retport.CleanupExecutionStore
	armed *bool
}

func (s cleanupFaultStore) Cleanup() retport.CleanupStore {
	return cleanupAckFault{s.CleanupExecutionStore.Cleanup(), s.armed}
}

func TestAccCleanupExecutorReference(t *testing.T) { cleanupExecutorContract(t, inmem.New()) }

func TestAccCleanupExecutorSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) { cleanupExecutorContract(t, newStore(t)) },
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func cleanupExecutorContract(t *testing.T, db harness.Store) {
	t.Helper()
	f := newCleanupExecutorFixture(t, db)
	ctx, caller, ids := f.ctx, f.caller, f.ids
	policies, executor, artifactAPI := f.policies, f.executor, f.artifacts
	module, store, now := f.module, f.store, f.now
	usage, ok := db.Repos().(artport.StorageUsage)
	if !ok {
		t.Fatal("execution fixture lacks normal storage usage")
	}
	initialBytes := storekit.Get[int64](t)(usage.RepositoryStorageBytes(ctx, "execution"))
	origin := storekit.Get[string](t)(db.Instance().ID(ctx))
	_, err := db.Events().RegisterConsumer(ctx, origin, "cleanup-proof")
	storekit.Must(t, err)
	deletedEvents := func() []int64 {
		t.Helper()
		rows, _, err := db.Events().Next(ctx, origin, "cleanup-proof", 100)
		storekit.Must(t, err)
		var found []int64
		for _, row := range rows {
			if row.Topic != artifactsapi.ArtifactDeleted {
				continue
			}
			var event artifactsapi.ArtifactEvent
			storekit.Must(t, json.Unmarshal(row.Payload, &event))
			if event.Item.Repository.Key != "execution" {
				continue
			}
			if event.Actor.Name != "admin" || event.Actor.System || event.Size <= 0 || event.SHA256 == "" {
				t.Fatal("cleanup event lost normal artifact facts or original actor")
			}
			found = append(found, event.Item.ID)
		}
		slices.Sort(found)
		return found
	}
	lease := func(run model.CleanupRun, holder string) work.WorkItem {
		t.Helper()
		item, taken, err := db.WorkQueue().LeaseKey(ctx, cleanup.WorkKind, strconv.FormatInt(run.ID, 10), work.WorkLease{Holder: holder, Now: now, Until: now.Add(time.Hour)})
		storekit.Must(t, err)
		if !taken {
			t.Fatal("cleanup run was not durably queued")
		}
		return item
	}
	dry, err := policies.ManualRun(caller, "execution-policy", true)
	storekit.Must(t, err)
	dryWork := lease(dry, "dry-worker")
	storekit.Must(t, executor.Execute(ctx, dryWork))
	dry = storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(ctx, dry.ID))
	if dry.Status != retapi.RunCompleted || dry.TotalItems != 2 || dry.CleanedItems != 0 || dry.CleanedSize != 0 {
		t.Fatalf("dry run effects were inaccurate: status=%s total=%d cleaned=%d", dry.Status, dry.TotalItems, dry.CleanedItems)
	}
	for _, id := range ids {
		_, err := db.Items().GetByID(ctx, id)
		storekit.Must(t, err)
	}
	storekit.Same(t, "dry run leaves logical usage", initialBytes, storekit.Get[int64](t)(usage.RepositoryStorageBytes(ctx, "execution")))
	if len(deletedEvents()) != 0 {
		t.Fatal("dry run emitted deletion effects")
	}
	storekit.Must(t, db.WorkQueue().Complete(ctx, dryWork.Kind, dryWork.Key, dryWork.Generation, now))
	actual, err := policies.ManualRun(caller, "execution-policy", false)
	storekit.Must(t, err)
	actualWork := lease(actual, "first-worker")
	fence := func(ctx context.Context) error {
		_, err := db.WorkQueue().GuardLease(ctx, actualWork.Kind, actualWork.Key, actualWork.Generation)
		return err
	}
	for {
		p := storekit.Get[model.CleanupProgress](t)(db.Cleanup().Progress(ctx, actual.ID))
		if p.Phase == model.CleanupRemoving {
			break
		}
		_, err := executor.Step(ctx, actual.ID, fence)
		storekit.Must(t, err)
	}
	before := storekit.Get[model.CleanupProgress](t)(db.Cleanup().Progress(ctx, actual.ID))
	armed := true
	faulty := executor
	faulty.Store = cleanupFaultStore{store, &armed}
	_, err = faulty.Step(ctx, actual.ID, fence)
	storekit.WantErr(t, err, errCleanupEffectAck)
	for _, id := range ids {
		_, err := db.Items().GetByID(ctx, id)
		storekit.Must(t, err)
	}
	storekit.Same(t, "failed acknowledgement rolls back usage", initialBytes, storekit.Get[int64](t)(usage.RepositoryStorageBytes(ctx, "execution")))
	if len(deletedEvents()) != 0 {
		t.Fatal("failed acknowledgement leaked deletion events")
	}
	storekit.Same(t, "failed acknowledgement rolls back continuation", before, storekit.Get[model.CleanupProgress](t)(db.Cleanup().Progress(ctx, actual.ID)))
	storekit.Must(t, func() error { _, err := executor.Step(ctx, actual.ID, fence); return err }())
	partial := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(ctx, actual.ID))
	if partial.CleanedItems != 1 {
		t.Fatal("first actual effect was not durably acknowledged", partial.CleanedItems)
	}
	storekit.Must(t, db.WorkQueue().Release(ctx, actualWork.Kind, actualWork.Key, actualWork.Generation, now))
	restartedWork := lease(actual, "restarted-worker")
	storekit.WantErr(t, executor.Execute(ctx, actualWork), kernel.ErrConflict)
	stale := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(ctx, actual.ID))
	if stale.CleanedItems != 1 || stale.Status.Terminal() {
		t.Fatal("old queue generation published an effect or a terminal result")
	}
	restarted := cleanup.Executor{Store: store, Policies: module.Policies(), Removal: artifactAPI.ItemRemoval}
	restarted.Policies.Now = func() time.Time { return now }
	storekit.Must(t, restarted.Execute(ctx, restartedWork))
	finished := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(ctx, actual.ID))
	if finished.Status != retapi.RunCompleted || finished.TotalItems != 2 || finished.CleanedItems != 2 || finished.CleanedSize != int64(len("real-one.bin")+len("real-two.bin")) {
		t.Fatalf("restart effects were inaccurate: status=%s total=%d cleaned=%d bytes=%d", finished.Status, finished.TotalItems, finished.CleanedItems, finished.CleanedSize)
	}
	storekit.Same(t, "actual deletion updates logical usage", int64(0), storekit.Get[int64](t)(usage.RepositoryStorageBytes(ctx, "execution")))
	storekit.Same(t, "exactly one durable deletion event per actual item", ids, deletedEvents())
	for _, id := range ids {
		_, err := db.Items().GetByID(ctx, id)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	}
	for _, name := range []string{"one.bin", "two.bin"} {
		trash := storekit.Get[[]artifactmodel.TrashEntry](t)(db.Trash().Find(ctx, "execution", "package/1/"+name))
		if len(trash) != 1 {
			t.Fatal("cleanup did not use ordinary recoverable trash", len(trash))
		}
	}
}
