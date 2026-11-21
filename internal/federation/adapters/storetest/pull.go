package storetest

import (
	"context"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func pullBinding() model.PullBinding {
	return model.PullBinding{
		TargetKey: "target", TargetRevision: "target-revision",
		Source: artifactsapi.PullRepository{Repository: artifactsapi.TransferRepository{
			Key: "remote", Incarnation: "remote-lifetime", Generation: "generation", PackageType: repo.Generic,
		}, Revision: strings.Repeat("a", 64)}, Credential: kernel.CredentialReference{Name: "author", TokenID: "original-token"}, Root: "selected",
	}
}

func pullFile(run model.PullRun) model.PullItem {
	return model.PullItem{
		Run: run.Reference, Action: model.PullFetch, Path: "selected/a", Version: 1, Phase: model.PullIntent, Updated: run.Started,
		Source: &artifactsapi.PullItem{
			Repository: run.Binding.Source, Selection: run.Binding.Selection(), Path: "selected/a", UpstreamPath: "selected/a",
			Size: 4, Actual: kernel.Checksums{SHA256: strings.Repeat("b", 64)}, Version: strings.Repeat("c", 64),
		},
	}
}

func runPullProgress(t *testing.T, newStore Factory) {
	t.Run("PullProgress", func(t *testing.T) {
		db := newStore(t)
		ctx, progress := t.Context(), db.PullProgress()
		run := storekit.Get[model.PullRun](t)(progress.Create(ctx, "pull", pullBinding(), storekit.At(1)))
		intent := pullFile(run)
		stored := storekit.Get[model.PullItem](t)(progress.CreateItem(ctx, intent))
		storekit.Same(t, "idempotent immutable run", run, storekit.Get[model.PullRun](t)(progress.Create(ctx, "pull", pullBinding(), storekit.At(9))))
		binding := pullBinding()
		binding.Credential.TokenID = "changed-token"
		_, err := progress.Create(ctx, "pull", binding, storekit.At(9))
		storekit.WantErr(t, err, kernel.ErrConflict)
		changed := kernel.Clone(intent)
		changed.Source.Size++
		_, err = progress.CreateItem(ctx, changed)
		storekit.WantErr(t, err, kernel.ErrConflict)
		settled := kernel.Clone(stored)
		settled.Version++
		settled.Phase, settled.Outcome = model.PullItemComplete, model.PullSucceeded
		settled.Cache = &artifactsapi.PullCached{Item: *settled.Source, Representation: strings.Repeat("d", 64), Version: strings.Repeat("e", 64)}
		storekit.WantErr(t, progress.AdvanceItem(ctx, stored.Version, settled), kernel.ErrInvalid)
		storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.AdvanceItem(ctx, stored.Version, settled) }))
		storekit.Same(t, "receipt retained", settled, storekit.Get[model.PullItem](t)(progress.Item(ctx, run.Reference, settled.Action, settled.Path)))
		storekit.Same(t, "intent retry retains completed cache", settled, storekit.Get[model.PullItem](t)(progress.CreateItem(ctx, intent)))
		pending, next, err := progress.Items(ctx, run.Reference, true, kernel.Page{Limit: 1})
		storekit.Must(t, err)
		if len(pending) != 0 || next != "" {
			t.Fatal("verified cache remained pending")
		}
		complete := run
		complete.Version++
		complete.SourceDone = true
		complete.Phase = model.PullComplete
		complete.Completed, complete.Updated = storekit.At(2), storekit.At(2)
		storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, run.Version, complete) }))
		storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, run.Version, complete) }), kernel.ErrConflict)
		storekit.Same(t, "completed source progress", complete, storekit.Get[model.PullRun](t)(progress.Run(ctx, run.Reference)))
	})
}
