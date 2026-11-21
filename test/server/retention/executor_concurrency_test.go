package retention_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
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
}
