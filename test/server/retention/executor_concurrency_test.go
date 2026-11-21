package retention_test

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
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

type cleanupPausedProgress struct {
	retport.CleanupStore
	store            retport.CleanupExecutionStore
	once             *sync.Once
	entered, release chan struct{}
}

func (s cleanupPausedProgress) Progress(ctx context.Context, id int64) (model.CleanupProgress, error) {
	p, err := s.CleanupStore.Progress(ctx, id)
	if err == nil && !s.store.InTx(ctx) {
		s.once.Do(func() {
			close(s.entered)
			select {
			case <-s.release:
			case <-ctx.Done():
				err = fmt.Errorf("paused cleanup read: %w", ctx.Err())
			}
		})
	}
	return p, err
}

type cleanupPausedStore struct {
	retport.CleanupExecutionStore
	progress cleanupPausedProgress
}

type cleanupPageBarrier struct {
	pages            atomic.Int64
	once             sync.Once
	entered, release chan struct{}
}

type cleanupPageStore struct {
	retport.CleanupExecutionStore
	barrier *cleanupPageBarrier
}

func (s cleanupPageStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	root, before := !s.InTx(ctx), s.barrier.pages.Load()
	err := s.CleanupExecutionStore.Tx(ctx, fn)
	if err == nil && root && s.barrier.pages.Load() > before {
		s.barrier.once.Do(func() {
			close(s.barrier.entered)
			select {
			case <-s.barrier.release:
			case <-ctx.Done():
				err = ctx.Err()
			}
		})
	}
	return err
}

type cleanupCountedPages struct {
	artifactsapi.PackageIndex
	barrier *cleanupPageBarrier
}

func (s cleanupCountedPages) Files(ctx context.Context, ref artifactsapi.IndexVersion, properties map[string][]string, page kernel.Page) ([]artifactsapi.IndexFile, string, error) {
	// Returning a smaller real page is permitted by the index contract. It
	// makes a complete lead scan cross several publication boundaries.
	page.Limit = min(page.Limit, 2)
	rows, next, err := s.PackageIndex.Files(ctx, ref, properties, page)
	s.barrier.pages.Add(1)
	return rows, next, err
}

func (s cleanupPausedStore) Cleanup() retport.CleanupStore { return s.progress }

func TestAccCleanupBlockedExecutionReference(t *testing.T) {
	cleanupBlockedExecution(t, func(*testing.T) harness.Store { return inmem.New() })
}

func TestAccCleanupBlockedExecutionSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			cleanupBlockedExecution(t, func(t *testing.T) harness.Store { return newStore(t) })
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func cleanupBlockedExecution(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *cleanupExecutorFixture, model.CleanupRun)
		status retapi.RunStatus
	}{
		{"stop", func(t *testing.T, f *cleanupExecutorFixture, run model.CleanupRun) {
			storekit.Must(t, f.policies.Stop(f.caller, run.ID))
		}, retapi.RunStopped},
		{"force disable", func(t *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun) {
			storekit.Must(t, f.policies.DisableAll(f.caller, ""))
		}, retapi.RunStopped},
		{"revoke caller credential", func(t *testing.T, f *cleanupExecutorFixture, _ model.CleanupRun) {
			user := storekit.Get[accesstest.User](t)(f.db.Users().Get(f.ctx, "admin"))
			user.PasswordHash = accesstest.HashPassword(t, "changed-while-blocked")
			storekit.Must(t, f.db.Users().Update(f.ctx, user))
		}, retapi.RunFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, newStore(t))
			run, w := f.selected(t)
			entered, release := make(chan struct{}), make(chan struct{})
			paused := cleanupPausedProgress{CleanupStore: f.store.Cleanup(), store: f.store, once: &sync.Once{}, entered: entered, release: release}
			executor := f.executor
			executor.Store = cleanupPausedStore{f.store, paused}
			done := make(chan struct{})
			var executionErr error
			go func() {
				executionErr = executor.Execute(f.ctx, w)
				close(done)
			}()
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer func() {
				unblock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("blocked cleanup worker did not exit")
				}
			}()
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal("worker did not reach the pre-batch barrier")
			}
			// A real administration/credential transaction commits after the
			// worker read its saved intent and before its next mutation guard.
			tc.change(t, f, run)
			unblock()
			select {
			case <-done:
			case <-f.ctx.Done():
				t.Fatal("worker did not settle the changed authority")
			}
			storekit.Must(t, executionErr)
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
			if finished.Status != tc.status || finished.CleanedItems != 0 {
				t.Fatal("blocked work retained stale deletion authority", finished.Status, finished.CleanedItems)
			}
			f.preserved(t)
		})
	}
	t.Run("stop between discovery pages", func(t *testing.T) {
		f := cleanupLargeVersion(t, newStore(t))
		policy, err := f.policies.Get(f.caller, "execution-policy", false)
		storekit.Must(t, err)
		policy.Criteria.CreatedBeforeInDays = 1
		_, err = f.policies.Update(f.caller, "execution-policy", policy)
		storekit.Must(t, err)
		r, err := f.policies.ManualRun(f.caller, "execution-policy", false)
		storekit.Must(t, err)
		w, taken, err := f.db.WorkQueue().
			LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(r.ID, 10), work.WorkLease{Holder: "paged-discovery", Now: f.now, Until: f.now.Add(time.Hour)})
		storekit.Must(t, err)
		if !taken {
			t.Fatal("discovery was not queued")
		}
		fence := func(ctx context.Context) error {
			_, err := f.db.WorkQueue().GuardLease(ctx, w.Kind, w.Key, w.Generation)
			return err
		}
		for range 100 {
			p := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, r.ID))
			if p.Phase == model.CleanupCandidates {
				break
			}
			_, err := f.executor.Step(f.ctx, r.ID, fence)
			storekit.Must(t, err)
		}
		barrier := &cleanupPageBarrier{entered: make(chan struct{}), release: make(chan struct{})}
		executor := f.executor
		store := cleanupPageStore{f.store, barrier}
		executor.Store, executor.Policies.Store = store, store
		executor.Policies.PackageIndex = cleanupCountedPages{executor.Policies.PackageIndex, barrier}
		done := make(chan struct{})
		var executionErr error
		go func() { executionErr = executor.Execute(f.ctx, w); close(done) }()
		var once sync.Once
		unblock := func() { once.Do(func() { close(barrier.release) }) }
		defer func() { unblock(); <-done }()
		select {
		case <-barrier.entered:
		case <-f.ctx.Done():
			t.Fatal("discovery did not finish its first page transaction")
		}
		if pages := barrier.pages.Load(); pages != 1 {
			t.Fatal("one write transaction spanned the complete version scan", pages)
		}
		storekit.Must(t, f.policies.Stop(f.caller, r.ID))
		unblock()
		select {
		case <-done:
		case <-f.ctx.Done():
			t.Fatal("discovery ignored a stop at its next page boundary")
		}
		storekit.Must(t, executionErr)
		finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, r.ID))
		if finished.Status != retapi.RunStopped || finished.CleanedItems != 0 || barrier.pages.Load() != 1 {
			t.Fatal("stopped discovery continued reading or published deletion")
		}
		f.preserved(t)
	})
}
