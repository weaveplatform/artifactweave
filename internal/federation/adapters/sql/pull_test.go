package sql_test

import (
	"context"
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
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccPullProgressReopenPreservesVerifiedCacheAndPendingTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pull-progress.db")
	open := func() *fedsql.Store {
		connection, err := sqlite.OpenDB(path, sqlite.Options{})
		storekit.Must(t, err)
		t.Cleanup(func() { storekit.Must(t, connection.Close()) })
		db := dialect.New(connection, sqlite.Dialect, nil)
		_, err = dialect.Migrate(t.Context(), db.DB(), db.Dialect(), []dialect.Module{fedsql.Module}, "")
		storekit.Must(t, err)
		storekit.Must(t, fedsql.Module.AfterMigrate(t.Context(), db))
		return fedsql.New(db)
	}
	first := open()
	binding := model.PullBinding{
		TargetKey: "target", TargetRevision: "configuration",
		Source: artifactsapi.PullRepository{Repository: artifactsapi.TransferRepository{
			Key: "remote", Incarnation: "owning-remote", Generation: "generation", PackageType: repo.Generic,
		}, Revision: strings.Repeat("a", 64)}, Credential: kernel.CredentialReference{Name: "author", TokenID: "original-token"}, Root: "selected",
	}
	run := storekit.Get[model.PullRun](t)(first.PullProgress().Create(t.Context(), "logical-pull", binding, time.Unix(1, 0)))
	intent := model.PullItem{
		Run: run.Reference, Action: model.PullFetch, Path: "selected/a", Phase: model.PullIntent, Version: 1, Updated: run.Started,
		Source: &artifactsapi.PullItem{
			Repository: binding.Source, Selection: binding.Selection(), Path: "selected/a", UpstreamPath: "source/a",
			Size: 4, Actual: kernel.Checksums{SHA256: strings.Repeat("b", 64)}, Version: strings.Repeat("c", 64),
		},
	}
	stored := storekit.Get[model.PullItem](t)(first.PullProgress().CreateItem(t.Context(), intent))
	stored.Version++
	stored.Phase, stored.Outcome = model.PullItemComplete, model.PullSucceeded
	stored.Cache = &artifactsapi.PullCached{Item: *stored.Source, Representation: strings.Repeat("d", 64), Version: strings.Repeat("e", 64)}
	next := run
	next.Version++
	next.SourceCursor, next.Updated = "next-source-page", time.Unix(2, 0)
	storekit.Must(t, first.Tx(t.Context(), func(ctx context.Context) error {
		if err := first.PullProgress().AdvanceItem(ctx, intent.Version, stored); err != nil {
			return err
		}
		return first.PullProgress().Advance(ctx, run.Version, next)
	}))
	storekit.Must(t, first.DB().Close())
	second := open()
	storekit.Same(t, "restart retains source cursor", next, storekit.Get[model.PullRun](t)(second.PullProgress().Run(t.Context(), run.Reference)))
	storekit.Same(t, "restart retains verified real-cache receipt", stored,
		storekit.Get[model.PullItem](t)(second.PullProgress().Item(t.Context(), run.Reference, intent.Action, intent.Path)))
	storekit.Same(t, "intent replay retains completed cache evidence", stored,
		storekit.Get[model.PullItem](t)(second.PullProgress().CreateItem(t.Context(), intent)))
	changed := binding
	changed.Source.Revision = strings.Repeat("f", 64)
	_, err := second.PullProgress().Create(t.Context(), "logical-pull", changed, time.Unix(9, 0))
	storekit.WantErr(t, err, kernel.ErrConflict)
}
