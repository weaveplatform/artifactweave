package retention_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
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
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccCleanupExecutorChangesReference(t *testing.T) {
	cleanupExecutorChanges(t, func(*testing.T) harness.Store { return inmem.New() })
}

func TestAccCleanupExecutorChangesSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			cleanupExecutorChanges(t, func(t *testing.T) harness.Store { return newStore(t) })
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func cleanupExecutorChanges(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	for _, phase := range []model.CleanupPhase{model.CleanupPackages, model.CleanupVersions, model.CleanupCandidates} {
		t.Run(string(phase), func(t *testing.T) {
			f := newCleanupExecutorFixture(t, newStore(t))
			r, err := f.policies.ManualRun(f.caller, "execution-policy", false)
			storekit.Must(t, err)
			w, taken, err := f.db.WorkQueue().
				LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10), work.WorkLease{Holder: "changed-parent", Now: f.now, Until: f.now.Add(work.DefaultLease)})
			storekit.Must(t, err)
			if !taken {
				t.Fatal("run not queued")
			}
			fence := func(ctx context.Context) error {
				_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
				return err
			}
			var p model.CleanupProgress
			for range 100 {
				p = storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
				if p.Phase == phase {
					break
				}
				_, err := f.executor.Step(f.ctx, r.ID, fence)
				storekit.Must(t, err)
			}
			if p.Phase != phase {
				t.Fatal("fixture did not reach parent phase")
			}
			switch phase {
			case model.CleanupPackages:
				storekit.Must(t, f.db.Repos().Delete(f.ctx, "execution"))
			case model.CleanupVersions:
				storekit.Must(t, f.db.Packages().Delete(f.ctx, p.Package.ID))
			case model.CleanupCandidates:
				storekit.Must(t, f.db.Packages().DeleteVersion(f.ctx, p.Version.ID))
			}
			storekit.Must(t, f.executor.Execute(f.ctx, w))
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if finished.Status != retapi.RunCompleted || finished.CleanedItems != 0 {
				t.Fatal("obsolete parent was retried or produced a deletion", finished.Status, finished.CleanedItems)
			}
			f.preserved(t)
		})
	}
	t.Run("skip trashcan", func(t *testing.T) {
		f := newCleanupExecutorFixture(t, newStore(t))
		p, err := f.policies.Get(f.caller, "execution-policy", false)
		storekit.Must(t, err)
		p.SkipTrashcan = true
		_, err = f.policies.Update(f.caller, "execution-policy", p)
		storekit.Must(t, err)
		r, w := f.selected(t)
		storekit.Must(t, f.executor.Execute(f.ctx, w))
		finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
		if finished.Status != retapi.RunCompleted || finished.CleanedItems != 2 {
			t.Fatal("purge did not acknowledge actual effects")
		}
		for _, id := range f.ids {
			_, err := f.db.Items().GetByID(f.ctx, id)
			storekit.WantErr(t, err, kernel.ErrNotFound)
		}
		for _, name := range []string{"one.bin", "two.bin"} {
			rows := storekit.Get[[]artifactmodel.TrashEntry](t)(f.db.Trash().Find(f.ctx, "execution", "package/1/"+name))
			if len(rows) != 0 {
				t.Fatal("skipTrashcan created recoverable trash")
			}
		}
	})
	t.Run("resume a multi-page version", func(t *testing.T) {
		f := cleanupLargeVersion(t, newStore(t))
		r, w := f.selected(t)
		fence := func(ctx context.Context) error {
			_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
			return err
		}
		for range 3 {
			_, err := f.executor.Step(f.ctx, r.ID, fence)
			storekit.Must(t, err)
		}
		partial := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
		if partial.CleanedItems != 3 || partial.Status.Terminal() {
			t.Fatal("partial version did not retain exactly three effects")
		}
		// Reconstruct the worker from module bindings, retaining only persisted
		// state and its current lease rather than an in-memory traversal.
		resumed := storekit.Get[cleanup.Executor](t)(f.module.Executor())
		resumed.Policies = f.policies
		storekit.Must(t, resumed.Execute(f.ctx, w))
		finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
		if finished.Status != retapi.RunCompleted || finished.TotalItems != 65 || finished.CleanedItems != 65 {
			t.Fatal("resumed multi-page version lost or duplicated effects", finished.Status, finished.TotalItems, finished.CleanedItems)
		}
		for _, id := range f.ids {
			_, err := f.db.Items().GetByID(f.ctx, id)
			storekit.WantErr(t, err, kernel.ErrNotFound)
		}
	})
	t.Run("invalidate a partially selected dry version", func(t *testing.T) {
		f := cleanupLargeVersion(t, newStore(t))
		r, err := f.policies.ManualRun(f.caller, "execution-policy", true)
		storekit.Must(t, err)
		w, taken, err := f.db.WorkQueue().
			LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10), work.WorkLease{Holder: "dry-worker", Now: f.now, Until: f.now.Add(time.Hour)})
		storekit.Must(t, err)
		if !taken {
			t.Fatal("dry run was not queued")
		}
		fence := func(ctx context.Context) error {
			_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
			return err
		}
		var p model.CleanupProgress
		for range 100 {
			p = storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
			if p.Phase == model.CleanupCandidates && p.FileAfter != "" {
				break
			}
			_, err := f.executor.Step(f.ctx, r.ID, fence)
			storekit.Must(t, err)
		}
		if p.Phase != model.CleanupCandidates || p.FileAfter == "" {
			t.Fatal("fixture did not persist a partial candidate page")
		}
		response := support.Do(t, f.handler, http.MethodGet, "/artifactory/execution/package/1/one.bin", support.Basic("admin", "pw"), "")
		if response.Code != http.StatusOK {
			t.Fatal("concurrent download failed", response.Code)
		}
		storekit.Must(t, f.executor.Execute(f.ctx, w))
		finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
		if finished.Status != retapi.RunCompleted || finished.CleanedItems != 0 {
			t.Fatal("changed dry version reported deletion or failed to settle")
		}
		rows, next, err := f.db.Cleanup().Entries(f.ctx, retport.CleanupEntryQuery{RunID: r.ID, VersionID: p.Version.ID, Kind: model.CleanupItemEntry}, kernel.Page{Limit: 100})
		storekit.Must(t, err)
		if len(rows) != 64 || next != "" {
			t.Fatal("dry version lost its first page of decision evidence", len(rows))
		}
		for _, row := range rows {
			if row.State != model.CleanupSkipped || row.Reason != string(cleanup.ReasonChanged) {
				t.Fatal("obsolete dry candidate remained selected", row.EntityID, row.State, row.Reason)
			}
		}
		f.preserved(t)
	})
}

func cleanupLargeVersion(t *testing.T, db harness.Store) *cleanupExecutorFixture {
	t.Helper()
	f := newCleanupExecutorFixture(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	f.ctx = ctx
	for i := range 63 {
		path := fmt.Sprintf("package/1/page-%03d.bin", i)
		response := support.Do(t, f.handler, http.MethodPut, "/artifactory/execution/"+path, support.Basic("admin", "pw"), "paged-content")
		if response.Code != http.StatusCreated {
			t.Fatal("large-version fixture upload failed", response.Code)
		}
		item := storekit.Get[artifactmodel.Item](t)(db.Items().Get(ctx, "execution", false, path))
		f.ids = append(f.ids, item.ID)
		storekit.Must(t, pkgindex.Link(ctx, db, pkgindex.Version{RepoKey: "execution", PackageType: "generic", Package: "package", Version: "1"}, item.ID, time.Now()))
	}
	return f
}
