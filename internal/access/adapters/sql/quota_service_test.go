package sql_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	accinmem "github.com/weaveplatform/artifactweave/internal/access/adapters/inmem"
	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func storageAdmissionContract(t *testing.T, newStore func(*testing.T) port.Store) {
	t.Helper()
	t.Run("parallel boundary", func(t *testing.T) {
		db, ctx := newStore(t), t.Context()
		p := model.Project{Key: "alpha", StorageQuotaBytes: 100}
		if err := db.Projects().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		var initialized atomic.Int32
		s := project.Service{Store: db, StorageBytes: func(context.Context, string) (int64, error) { initialized.Add(1); return 70, nil }}
		publish := func(ctx context.Context) error {
			current, err := db.Projects().Get(ctx, "alpha")
			if err != nil {
				return err
			}
			current.Description += "published;"
			return db.Projects().Update(ctx, current)
		}
		start, results := make(chan struct{}), make(chan error, 2)
		var writers sync.WaitGroup
		for range 2 {
			writers.Go(func() { <-start; results <- s.ChangeStorage(ctx, "alpha", 20, publish) })
		}
		close(start)
		writers.Wait()
		close(results)
		accepted, refused := 0, 0
		for err := range results {
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, project.ErrStorageQuota):
				refused++
			default:
				t.Fatal(err)
			}
		}
		state, found, err := db.Projects().Storage(ctx, "alpha")
		if err != nil || !found || state.Bytes != 90 || initialized.Load() != 1 || accepted != 1 || refused != 1 {
			t.Fatal("parallel publications exceeded the quota or reseeded initialized usage", state, found, initialized.Load(), accepted, refused, err)
		}
		p, err = db.Projects().Get(ctx, "alpha")
		if err != nil || p.Description != "published;" {
			t.Fatal("refused publication left effects", p.Description, err)
		}
		p.StorageQuotaBytes = 50
		if err := db.Projects().Update(ctx, p); err != nil {
			t.Fatal(err)
		}
		for _, delta := range []int64{0, -1} {
			if err := s.ChangeStorage(ctx, "alpha", delta, publish); err != nil {
				t.Fatal("lowering below usage blocked a non-growing publication", err)
			}
		}
		if err := s.ChangeStorage(ctx, "alpha", 1, publish); !errors.Is(err, project.ErrStorageQuota) {
			t.Fatal("lowered quota permitted growth", err)
		}
	})
	t.Run("publication and handoff rollback", func(t *testing.T) {
		db, ctx := newStore(t), t.Context()
		if err := db.Projects().Create(ctx, model.Project{Key: "alpha", StorageQuotaBytes: 100, QuotaEmail: true}); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("failed durable publication") //nolint:err113 // matched by identity
		s := project.Service{Store: db, StorageBytes: func(context.Context, string) (int64, error) { return 70, nil }}
		if err := s.ChangeStorage(ctx, "alpha", 5, nil); !errors.Is(err, kernel.ErrStore) {
			t.Fatal("configured warning silently lost its durable handoff", err)
		}
		var delivered model.ProjectStorageWarning
		s.StorageWarning = func(ctx context.Context, warning model.ProjectStorageWarning) error {
			delivered = warning
			p, err := db.Projects().Get(ctx, "alpha")
			if err != nil {
				return err
			}
			p.Description = warning.WarningKey
			if err := db.Projects().Update(ctx, p); err != nil {
				return err
			}
			return boom
		}
		publish := func(ctx context.Context) error {
			p, err := db.Projects().Get(ctx, "alpha")
			if err != nil {
				return err
			}
			p.DisplayName = "uncommitted publication"
			return db.Projects().Update(ctx, p)
		}
		if err := s.ChangeStorage(ctx, "alpha", 5, publish); !errors.Is(err, boom) {
			t.Fatal("handoff failure did not fail publication", err)
		}
		state, found, err := db.Projects().Storage(ctx, "alpha")
		if err != nil || found || state != (model.ProjectStorage{}) {
			t.Fatal("failed publication committed accounting initialization", state, found, err)
		}
		p, err := db.Projects().Get(ctx, "alpha")
		if err != nil || p.Description != "" || p.DisplayName != "" {
			t.Fatal("handoff failure left publication effects", p, err)
		}
		if delivered.WarningKey == "" || delivered.Incarnation == "" || delivered.Bytes != 75 || delivered.Limit != 100 {
			t.Fatal("warning handoff lost its self-contained identity", delivered)
		}
		var warnings []model.ProjectStorageWarning
		s.StorageWarning = func(_ context.Context, warning model.ProjectStorageWarning) error {
			warnings = append(warnings, warning)
			return nil
		}
		for _, delta := range []int64{5, 1, 0, -2, 1} {
			if err := s.ChangeStorage(ctx, "alpha", delta, nil); err != nil {
				t.Fatal(err)
			}
		}
		state, found, err = db.Projects().Storage(ctx, "alpha")
		if err != nil || !found || state.Bytes != 75 || len(warnings) != 2 || warnings[0].WarningKey == warnings[1].WarningKey || state.WarningKey != warnings[1].WarningKey {
			t.Fatal("warning identity was not stable above threshold or rearmed below it", state, warnings, err)
		}
		err = db.Tx(ctx, func(ctx context.Context) error {
			if err := s.ChangeStorage(ctx, "alpha", -1, publish); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal("enclosing rollback lost its error", err)
		}
		after, _, err := db.Projects().Storage(ctx, "alpha")
		if err != nil || after != state {
			t.Fatal("enclosing rollback changed usage or warning identity", after, state, err)
		}
	})
}

func TestUnitProjectStorageTransactions(t *testing.T) {
	storageAdmissionContract(t, func(*testing.T) port.Store { return accinmem.New(memtx.New()) })
}

func TestAccProjectStorageTransactions(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			storageAdmissionContract(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}
