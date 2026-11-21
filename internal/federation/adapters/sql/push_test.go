package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func pushSQLBinding() model.PushBinding {
	return model.PushBinding{
		TargetKey: "target", TargetRevision: "target-revision", TransportRevision: "transport-revision",
		Source: artifactsapi.TransferRepository{
			Key: "source", Incarnation: "source-lifetime", Generation: "source-revision", PackageType: repo.Generic,
		},
		Credential: kernel.CredentialReference{Name: "reader", TokenID: "original-source-token"},
		Include:    "**",
	}
}

func pushSQLIntent(r model.PushRun) model.PushItem {
	return model.PushItem{
		Run: r.Reference, Action: model.PushUpload, Path: "item.pkg", Version: 1, Phase: model.PushIntent, Updated: r.Started,
		Source: &artifactsapi.TransferItem{
			Repository: r.Binding.Source, ID: 7, Path: "item.pkg", Size: 6,
			Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Version: strings.Repeat("b", 64),
		},
	}
}

func TestAccPushProgressReopen(t *testing.T) {
	ctx, path := t.Context(), filepath.Join(t.TempDir(), "push.db")
	open := func() *fedsql.Store {
		connection, err := sqlite.OpenDB(path, sqlite.Options{})
		storekit.Must(t, err)
		t.Cleanup(func() { storekit.Must(t, connection.Close()) })
		db := dialect.New(connection, sqlite.Dialect, nil)
		_, err = dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{fedsql.Module}, "")
		storekit.Must(t, err)
		storekit.Must(t, fedsql.Module.AfterMigrate(ctx, db))
		return fedsql.New(db)
	}
	first := open()
	r := storekit.Get[model.PushRun](t)(first.PushProgress().Create(ctx, "run", pushSQLBinding(), time.Unix(1, 0)))
	intent := pushSQLIntent(r)
	i := storekit.Get[model.PushItem](t)(first.PushProgress().CreateItem(ctx, intent))
	i.Version++
	i.Phase, i.VerifiedTarget = model.PushContentVerified, &model.PushTargetSnapshot{Actual: i.Source.Actual, Size: i.Source.Size}
	r.Version++
	r.SourceCursor = "next-source-page"
	storekit.Must(t, first.Tx(ctx, func(ctx context.Context) error {
		if err := first.PushProgress().Advance(ctx, 1, r); err != nil {
			return err
		}
		return first.PushProgress().AdvanceItem(ctx, 1, i)
	}))
	storekit.Must(t, first.DB().Close())
	second := open()
	storekit.Same(t, "reopened run", r, storekit.Get[model.PushRun](t)(second.PushProgress().Run(ctx, r.Reference)))
	storekit.Same(t, "reopened verified evidence", i, storekit.Get[model.PushItem](t)(second.PushProgress().Item(ctx, r.Reference, i.Action, i.Path)))
	storekit.Same(t, "retry retains original incarnation and cursor", r,
		storekit.Get[model.PushRun](t)(second.PushProgress().Create(ctx, "run", pushSQLBinding(), time.Unix(9, 0))))
	storekit.Same(t, "retry retains acknowledged content", i, storekit.Get[model.PushItem](t)(second.PushProgress().CreateItem(ctx, intent)))
	changed := pushSQLBinding()
	changed.TargetRevision = "recreated-target"
	_, err := second.PushProgress().Create(ctx, "run", changed, time.Unix(9, 0))
	storekit.WantErr(t, err, kernel.ErrConflict)
}

func TestAccPushProgressWriteFaults(t *testing.T) {
	for _, tc := range []struct{ event, table string }{
		{"INSERT", "replication_push_runs"},
		{"INSERT", "replication_push_items"},
		{"UPDATE", "replication_push_runs"},
		{"UPDATE", "replication_push_items"},
	} {
		t.Run(tc.event+" "+tc.table, func(t *testing.T) {
			ctx := t.Context()
			s := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			progress := s.PushProgress()
			r := storekit.Get[model.PushRun](t)(progress.Create(ctx, "run", pushSQLBinding(), time.Unix(1, 0)))
			i := storekit.Get[model.PushItem](t)(progress.CreateItem(ctx, pushSQLIntent(r)))
			sqltest.Exec(t, s.DB(), sqltest.FailTrigger(tc.event, tc.table))
			nextRun := r
			nextRun.Version++
			nextRun.SourceCursor = "must-not-commit"
			nextItem := kernel.Clone(i)
			nextItem.Version++
			nextItem.Phase = model.PushContentVerified
			nextItem.VerifiedTarget = &model.PushTargetSnapshot{Actual: i.Source.Actual, Size: i.Source.Size}
			err := s.Tx(ctx, func(ctx context.Context) error {
				switch {
				case tc.table == "replication_push_runs" && tc.event == "INSERT":
					_, err := progress.Create(ctx, "new-run", pushSQLBinding(), time.Unix(2, 0))
					return err
				case tc.event == "INSERT":
					fresh := kernel.Clone(i)
					fresh.Path, fresh.Source.Path = "another.pkg", "another.pkg"
					_, err := progress.CreateItem(ctx, fresh)
					return err
				default:
					if err := progress.Advance(ctx, r.Version, nextRun); err != nil {
						return err
					}
					return progress.AdvanceItem(ctx, i.Version, nextItem)
				}
			})
			if err == nil || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("write fault was hidden: %v", err)
			}
			storekit.Same(t, "failed write kept cursor", r, storekit.Get[model.PushRun](t)(progress.Run(ctx, r.Reference)))
			storekit.Same(t, "failed write kept evidence", i, storekit.Get[model.PushItem](t)(progress.Item(ctx, r.Reference, i.Action, i.Path)))
			_, err = progress.Item(ctx, r.Reference, model.PushUpload, "another.pkg")
			storekit.WantErr(t, err, kernel.ErrNotFound)
			_, err = progress.Run(ctx, model.PushReference{ID: "new-run", Incarnation: "never-created"})
			storekit.WantErr(t, err, kernel.ErrNotFound)
		})
	}
}
