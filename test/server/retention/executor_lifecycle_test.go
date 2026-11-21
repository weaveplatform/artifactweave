package retention_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	artifactsmodule "github.com/weaveplatform/artifactweave/internal/artifacts"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	retport "github.com/weaveplatform/artifactweave/internal/retention/core/port"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type cleanupExecutorFixture struct {
	ctx, caller context.Context
	db          harness.Store
	store       retport.CleanupExecutionStore
	handler     http.Handler
	artifacts   artifactsapi.API
	module      *retention.Module
	policies    cleanup.Service
	executor    cleanup.Executor
	queue       *work.Queue
	ids         []int64
	now         time.Time
}

type cleanupBeforeAcknowledgement struct {
	artifactsapi.ItemRemoval
	before func()
}

type cleanupDeniedItem struct {
	artifactsapi.ItemRemoval
	id int64
}

var errPersistentCleanupRemoval = errors.New("injected persistent item store fault")

type cleanupPersistentItemFault struct {
	artifactsapi.ItemRemoval
	id            int64
	beforeFailure func()
}

func (r cleanupPersistentItemFault) Remove(
	ctx context.Context,
	request artifactsapi.RemovalRequest,
	validate func(context.Context) error,
	record func(context.Context, artifactsapi.RemovalResult) error,
) (artifactsapi.RemovalResult, error) {
	if request.Item.ID == r.id {
		if r.beforeFailure != nil {
			r.beforeFailure()
		}
		return artifactsapi.RemovalResult{}, errPersistentCleanupRemoval
	}
	return r.ItemRemoval.Remove(ctx, request, validate, record)
}

type cleanupFinalOutcomeFault struct {
	retport.CleanupStore
	armed *bool
}

func (s cleanupFinalOutcomeFault) AdvanceProgress(ctx context.Context, revision int64, p model.CleanupProgress) error {
	if *s.armed && p.Phase == model.CleanupFailing {
		return errPersistentCleanupRemoval
	}
	return s.CleanupStore.AdvanceProgress(ctx, revision, p)
}

type cleanupFinalOutcomeStore struct {
	retport.CleanupExecutionStore
	armed *bool
}

func (s cleanupFinalOutcomeStore) Cleanup() retport.CleanupStore {
	return cleanupFinalOutcomeFault{s.CleanupExecutionStore.Cleanup(), s.armed}
}

func (r cleanupDeniedItem) Remove(
	ctx context.Context,
	request artifactsapi.RemovalRequest,
	validate func(context.Context) error,
	record func(context.Context, artifactsapi.RemovalResult) error,
) (artifactsapi.RemovalResult, error) {
	if request.Item.ID == r.id {
		return artifactsapi.RemovalResult{}, kernel.ErrForbidden
	}
	return r.ItemRemoval.Remove(ctx, request, validate, record)
}

func (r cleanupBeforeAcknowledgement) Remove(
	ctx context.Context,
	request artifactsapi.RemovalRequest,
	validate func(context.Context) error,
	record func(context.Context, artifactsapi.RemovalResult) error,
) (artifactsapi.RemovalResult, error) {
	return r.ItemRemoval.Remove(ctx, request, validate, func(ctx context.Context, result artifactsapi.RemovalResult) error {
		r.before()
		return record(ctx, result)
	})
}

func newCleanupExecutorFixture(t *testing.T, db harness.Store) *cleanupExecutorFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	f := &cleanupExecutorFixture{ctx: ctx, db: db, now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	support.Seed(t, db)
	storekit.Must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "execution", Class: repo.Local, PackageType: repo.Generic})))
	access := accesstest.Over(t, db).API()
	blobs := storekit.Get[*fs.Store](t)(fs.New(t.TempDir()))
	queue := storekit.Get[*work.Queue](t)(work.New(db, work.Options{}))
	f.queue = queue
	artStore, ok := db.(artifactsmodule.Store)
	if !ok {
		t.Fatal("executor fixture has no artifacts store")
	}
	art := storekit.Get[*artifactsmodule.Module](
		t,
	)(
		artifactsmodule.New(artifactsmodule.Deps{Store: artStore, Blobs: blobs, Access: access, Work: queue, Settings: config.Default().Artifacts()}),
	)
	t.Cleanup(art.Close)
	storekit.Must(t, art.RegisterWork(ctx))
	f.artifacts = art.API()
	auth := storekit.Get[*authn.Authenticator](t)(authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: support.TestHash, Groups: db.Groups()}))
	f.handler = harness.Router(harness.Config{Store: db, Blobs: blobs, Auth: auth, Work: queue, FormatSettings: art.FormatSettings()})
	for _, name := range []string{"one.bin", "two.bin"} {
		path := "package/1/" + name
		response := support.Do(t, f.handler, http.MethodPut, "/artifactory/execution/"+path, support.Basic("admin", "pw"), "real-"+name)
		if response.Code != http.StatusCreated {
			t.Fatal("executor fixture upload failed", response.Code)
		}
		item := storekit.Get[artifactmodel.Item](t)(db.Items().Get(ctx, "execution", false, path))
		f.ids = append(f.ids, item.ID)
		storekit.Must(t, pkgindex.Link(ctx, db, pkgindex.Version{RepoKey: "execution", PackageType: repo.Generic, Package: "package", Version: "1"}, item.ID, time.Now()))
	}
	pkg := storekit.Get[artifactmodel.Package](t)(db.Packages().Find(ctx, "execution", "package"))
	_, err := db.Packages().PutVersion(ctx, artifactmodel.PackageVersion{PackageID: pkg.ID, Version: "2"})
	storekit.Must(t, err)
	retStore, ok := db.(retention.Store)
	if !ok {
		t.Fatal("executor fixture has no retention store")
	}
	f.module = storekit.Get[*retention.Module](
		t,
	)(
		retention.New(
			retention.Deps{
				Store:        retStore,
				Access:       access,
				Repositories: repos.NewProjectRepositoryLookup(artStore),
				PackageIndex: f.artifacts.PackageIndex,
				ItemRemoval:  f.artifacts.ItemRemoval,
			},
		),
	)
	f.policies = f.module.Policies()
	f.policies.Now = func() time.Time { return f.now }
	principal := storekit.Get[kernel.Principal](t)(access.Authenticator.Basic(ctx, "admin", "pw"))
	f.caller = kernel.WithPrincipal(ctx, principal)
	_, err = f.policies.Create(
		f.caller,
		"execution-policy",
		model.CleanupPolicy{
			Key:             "execution-policy",
			DurationMinutes: 1,
			Criteria: model.CleanupCriteria{
				PackageTypes:     []string{"generic"},
				Repos:            []string{"execution"},
				IncludedPackages: []string{"**"},
				IncludedProjects: []string{"default"},
			},
		},
	)
	storekit.Must(t, err)
	storekit.Must(t, f.policies.Enable(f.caller, "execution-policy", true))
	f.store, ok = db.(retport.CleanupExecutionStore)
	if !ok {
		t.Fatal("executor fixture has no execution store")
	}
	f.executor = storekit.Get[cleanup.Executor](t)(f.module.Executor())
	f.executor.Policies = f.policies
	return f
}

func (f *cleanupExecutorFixture) selected(t *testing.T) (model.CleanupRun, work.WorkItem) {
	t.Helper()
	r, err := f.policies.ManualRun(f.caller, "execution-policy", false)
	storekit.Must(t, err)
	w, taken, err := f.db.WorkQueue().
		LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10), work.WorkLease{Holder: "lifecycle-worker", Now: f.now, Until: f.now.Add(time.Hour)})
	storekit.Must(t, err)
	if !taken {
		t.Fatal("cleanup admission was not durably queued")
	}
	fence := func(ctx context.Context) error {
		_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
		return err
	}
	for range 100 {
		p := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
		if p.Phase == model.CleanupRemoving {
			return r, w
		}
		_, err := f.executor.Step(f.ctx, r.ID, fence)
		storekit.Must(t, err)
	}
	t.Fatal("bounded fixture never reached its selected deletion step")
	return r, w
}

func (f *cleanupExecutorFixture) preserved(t *testing.T) {
	t.Helper()
	for _, id := range f.ids {
		_, err := f.db.Items().GetByID(f.ctx, id)
		storekit.Must(t, err)
	}
	for _, name := range []string{"one.bin", "two.bin"} {
		trash := storekit.Get[[]artifactmodel.TrashEntry](t)(f.db.Trash().Find(f.ctx, "execution", "package/1/"+name))
		if len(trash) != 0 {
			t.Fatal("refused cleanup published trash effects")
		}
	}
}

func TestAccCleanupExecutorLifecycleReference(t *testing.T) {
	cleanupExecutorLifecycle(t, func(*testing.T) harness.Store { return inmem.New() })
}

func TestAccCleanupExecutorLifecycleSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			cleanupExecutorLifecycle(t, func(t *testing.T) harness.Store { return newStore(t) })
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func cleanupExecutorLifecycle(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	for _, tc := range []struct {
		name     string
		exercise func(*testing.T, *cleanupExecutorFixture, model.CleanupRun, work.WorkItem)
		status   retapi.RunStatus
		cleaned  int64
	}{
		{"stop", func(t *testing.T, f *cleanupExecutorFixture, r model.CleanupRun, _ work.WorkItem) {
			storekit.Must(t, f.policies.Stop(f.caller, r.ID))
		}, retapi.RunStopped, 0},
		{"force disable", func(t *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun, _ work.WorkItem) {
			storekit.Must(t, f.policies.DisableAll(f.caller, ""))
		}, retapi.RunStopped, 0},
		{"duration", func(_ *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun, _ work.WorkItem) {
			f.now = f.now.Add(time.Minute)
		}, retapi.RunStopped, 0},
		{"duration during acknowledgement", func(_ *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun, _ work.WorkItem) {
			f.executor.Removal = cleanupBeforeAcknowledgement{ItemRemoval: f.executor.Removal, before: func() { f.now = f.now.Add(time.Minute) }}
		}, retapi.RunStopped, 0},
		{"ordinary disable retains admitted run", func(t *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun, _ work.WorkItem) {
			storekit.Must(t, f.policies.Enable(f.caller, "execution-policy", false))
		}, retapi.RunCompleted, 2},
		{"password rotation", func(t *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun, _ work.WorkItem) {
			u := storekit.Get[accesstest.User](t)(f.db.Users().Get(f.ctx, "admin"))
			u.PasswordHash = accesstest.HashPassword(t, "rotated-password")
			storekit.Must(t, f.db.Users().Update(f.ctx, u))
		}, retapi.RunFailed, 0},
		{"newer overwrite", func(t *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun, _ work.WorkItem) {
			response := support.Do(t, f.handler, http.MethodPut, "/artifactory/execution/package/1/one.bin", support.Basic("admin", "pw"), "replacement")
			if response.Code != http.StatusCreated {
				t.Fatal("overwrite failed", response.Code)
			}
		}, retapi.RunCompleted, 0},
		{"new download", func(t *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun, _ work.WorkItem) {
			response := support.Do(t, f.handler, http.MethodGet, "/artifactory/execution/package/1/one.bin", support.Basic("admin", "pw"), "")
			if response.Code != http.StatusOK || response.Body.String() != "real-one.bin" {
				t.Fatal("download fixture failed", response.Code)
			}
		}, retapi.RunCompleted, 0},
		{"publication refusal", func(t *testing.T, f *cleanupExecutorFixture, r model.CleanupRun, _ work.WorkItem) {
			_, err := f.executor.Step(f.ctx, r.ID, func(context.Context) error { return kernel.ErrConflict })
			storekit.WantErr(t, err, kernel.ErrConflict)
			f.preserved(t)
			current := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if current.CleanedItems != 0 || current.Status.Terminal() {
				t.Fatal("refused final fence published progress")
			}
		}, retapi.RunCompleted, 2},
		{"partial item refusal", func(t *testing.T, f *cleanupExecutorFixture, r model.CleanupRun, w work.WorkItem) {
			f.executor.Removal = cleanupDeniedItem{ItemRemoval: f.executor.Removal, id: f.ids[0]}
			_, err := f.executor.Step(f.ctx, r.ID, func(ctx context.Context) error {
				_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
				return err
			})
			storekit.Must(t, err)
			current := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if current.Status != retapi.RunInProgressWithErrors || current.CleanedItems != 0 {
				t.Fatal("per-item refusal did not record active partial failure")
			}
		}, retapi.RunCompletedWithErrors, 1},
		{"shutdown", func(t *testing.T, f *cleanupExecutorFixture, r model.CleanupRun, w work.WorkItem) {
			ctx, cancel := context.WithCancel(f.ctx)
			cancel()
			storekit.WantErr(t, f.executor.Execute(ctx, w), context.Canceled)
			run := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if run.Status.Terminal() || run.CleanedItems != 0 {
				t.Fatal("shutdown falsely completed cleanup")
			}
			f.preserved(t)
		}, retapi.RunCompleted, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, newStore(t))
			r, w := f.selected(t)
			tc.exercise(t, f, r, w)
			storekit.Must(t, f.executor.Execute(f.ctx, w))
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if finished.Status != tc.status || finished.CleanedItems != tc.cleaned {
				t.Fatalf("status=%s cleaned=%d, want %s/%d", finished.Status, finished.CleanedItems, tc.status, tc.cleaned)
			}
			if tc.cleaned == 0 {
				f.preserved(t)
			}
		})
	}
	for _, tc := range []struct {
		name                    string
		refuseOutcome, duration bool
	}{
		{"record partial failure", false, false},
		{"retry unavailable outcome", true, false},
		{"duration expires during final failed attempt", false, true},
	} {
		t.Run("exhausted retries "+tc.name, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, newStore(t))
			response := support.Do(t, f.handler, http.MethodPut, "/artifactory/execution/package/1/three.bin", support.Basic("admin", "pw"), "real-three.bin")
			if response.Code != http.StatusCreated {
				t.Fatal("third-file fixture failed", response.Code)
			}
			third := storekit.Get[artifactmodel.Item](t)(f.db.Items().Get(f.ctx, "execution", false, "package/1/three.bin"))
			f.ids = append(f.ids, third.ID)
			storekit.Must(t, pkgindex.Link(f.ctx, f.db, pkgindex.Version{RepoKey: "execution", PackageType: repo.Generic, Package: "package", Version: "1"}, third.ID, f.now))
			r, w := f.selected(t)
			_, err := f.executor.Step(f.ctx, r.ID, func(ctx context.Context) error {
				_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
				return err
			})
			storekit.Must(t, err)
			storekit.Must(t, f.db.WorkQueue().Release(f.ctx, w.Kind, w.Key, w.Generation, f.now))
			executor := f.executor
			failed := 0
			executor.Removal = cleanupPersistentItemFault{ItemRemoval: executor.Removal, id: f.ids[1], beforeFailure: func() {
				failed++
				if tc.duration && failed == 2 {
					f.now = f.now.Add(time.Minute)
				}
			}}
			armed := tc.refuseOutcome
			executor.Store = cleanupFinalOutcomeStore{f.store, &armed}
			queue := storekit.Get[*work.Queue](
				t,
			)(
				work.New(f.db, work.Options{MaxAttempts: 2, Lease: time.Hour, Now: func() time.Time { return f.now }, Backoff: func(int) time.Duration { return 0 }}),
			)
			storekit.Must(t, queue.Handle(cleanup.WorkKind, executor.Execute))
			process := func() {
				t.Helper()
				n, err := queue.RunDue(f.ctx, cleanup.WorkKind)
				storekit.Must(t, err)
				if n != 1 {
					t.Fatal("real queue did not execute its pending cleanup", n)
				}
			}
			process()
			pending := storekit.Get[work.WorkItem](t)(f.db.WorkQueue().Get(f.ctx, w.Kind, w.Key))
			if pending.State != work.WorkPending || pending.Attempts != 1 {
				t.Fatal("ordinary failed attempt lost retry semantics")
			}
			process()
			if tc.refuseOutcome {
				pending = storekit.Get[work.WorkItem](t)(f.db.WorkQueue().Get(f.ctx, w.Kind, w.Key))
				unchanged := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
				if pending.State != work.WorkPending || pending.Attempts != 2 || unchanged.Status.Terminal() || unchanged.CleanedItems != 1 {
					t.Fatal("unrecorded final outcome was parked, lost or falsely completed")
				}
				armed = false
				process()
			}
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
			if tc.duration {
				if finished.Status != retapi.RunStopped || finished.CleanedItems != 1 {
					t.Fatal("final failed attempt overrode the elapsed duration", finished.Status, finished.CleanedItems)
				}
				p := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
				if p.Reason != "duration_limit" {
					t.Fatal("elapsed duration lost its terminal reason", p.Reason)
				}
				return
			}
			if finished.Status != retapi.RunFailed || finished.TotalItems != 3 || finished.CleanedItems != 1 {
				t.Fatal("retry exhaustion lost accurate partial failure", finished.Status, finished.TotalItems, finished.CleanedItems)
			}
			_, err = f.db.WorkQueue().Get(f.ctx, w.Kind, w.Key)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			rows, next, err := f.db.Cleanup().Entries(f.ctx, retport.CleanupEntryQuery{RunID: r.ID, Kind: model.CleanupItemEntry}, kernel.Page{Limit: 100})
			storekit.Must(t, err)
			if len(rows) != 3 || next != "" || rows[0].State != model.CleanupDeleted || rows[1].State != model.CleanupEntryFailed || rows[2].State != model.CleanupSkipped ||
				rows[1].Reason != "work_retries_exhausted" ||
				rows[2].Reason != "run_failed" {
				t.Fatal("partial run did not retain reasoned item outcomes")
			}
			for _, id := range f.ids[1:] {
				_, err := f.db.Items().GetByID(f.ctx, id)
				storekit.Must(t, err)
			}
			_, err = f.policies.ManualRun(f.caller, "execution-policy", false)
			storekit.Must(t, err)
		})
	}
}
