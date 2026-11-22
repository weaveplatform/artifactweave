package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
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
	for _, tc := range []struct {
		name string
		run  func(*testing.T, port.PullStore)
	}{
		{"invalid lifetimes and operations", pullInvalid},
		{"unfinished evidence and terminal lifetimes", pullLifecycle},
		{"bounded pending recovery pages", pullPages},
		{"rollback", pullRollback},
	} {
		t.Run("PullProgress/"+tc.name, func(t *testing.T) { tc.run(t, newStore(t)) })
	}
}

func pullInvalid(t *testing.T, s port.PullStore) {
	ctx, p := t.Context(), s.PullProgress()
	run := storekit.Get[model.PullRun](t)(p.Create(ctx, "run", pullBinding(), storekit.At(1)))
	_, err := p.Create(ctx, "", pullBinding(), storekit.At(1))
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = p.Run(ctx, model.PullReference{})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	stale := run.Reference
	stale.Incarnation = "another-lifetime"
	_, err = p.Run(ctx, stale)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	bad := run
	bad.Version++
	storekit.WantErr(t, p.Advance(ctx, run.Version, bad), kernel.ErrInvalid)
	bad.Reference = model.PullReference{}
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, bad) }), kernel.ErrInvalid)
	bad.Reference = stale
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, bad) }), kernel.ErrConflict)
	bad.Reference, bad.Phase = run.Reference, "future"
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, bad) }), kernel.ErrInvalid)
	for _, tc := range []struct {
		name string
		edit func(*model.PullItem)
		want error
	}{
		{"invalid reference", func(i *model.PullItem) { i.Run.ID = "" }, kernel.ErrInvalid},
		{"invalid path", func(i *model.PullItem) { i.Path = "../outside" }, kernel.ErrInvalid},
		{"missing lifetime", func(i *model.PullItem) { i.Run = stale }, kernel.ErrNotFound},
		{"missing source", func(i *model.PullItem) { i.Source = nil }, kernel.ErrInvalid},
		{"noninitial version", func(i *model.PullItem) { i.Version++ }, kernel.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := pullFile(run)
			tc.edit(&i)
			_, err := p.CreateItem(ctx, i)
			storekit.WantErr(t, err, tc.want)
		})
	}
	_, err = p.Item(ctx, model.PullReference{}, model.PullFetch, "selected/a")
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = p.Item(ctx, stale, model.PullFetch, "selected/a")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = p.Item(ctx, run.Reference, "future", "selected/a")
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = p.Item(ctx, run.Reference, model.PullFetch, "selected/missing")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	for _, after := range []string{"%%%", kernel.Cursor("wrong", "shape")} {
		_, _, err = p.Items(ctx, run.Reference, true, kernel.Page{After: after})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}
	_, _, err = p.Items(ctx, model.PullReference{}, true, kernel.Page{})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, _, err = p.Items(ctx, stale, true, kernel.Page{})
	storekit.WantErr(t, err, kernel.ErrNotFound)
	i := pullFile(run)
	i.Run = model.PullReference{}
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, 1, i) }), kernel.ErrInvalid)
	i.Run = stale
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, 1, i) }), kernel.ErrConflict)
	i.Run = run.Reference
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, 1, i) }), kernel.ErrConflict)
	storekit.Get[model.PullItem](t)(p.CreateItem(ctx, i))
	i.Version++
	i.Phase = "future"
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, 1, i) }), kernel.ErrInvalid)
	// Rejected updates cannot consume a version or poison the recoverable intent.
	storekit.Same(t, "rejected evidence retained intent", pullFile(run), storekit.Get[model.PullItem](t)(p.Item(ctx, run.Reference, model.PullFetch, "selected/a")))
}

func pullLifecycle(t *testing.T, s port.PullStore) {
	ctx, p := t.Context(), s.PullProgress()
	binding := pullBinding()
	binding.SyncDeletes = true
	run := storekit.Get[model.PullRun](t)(p.Create(ctx, "run", binding, storekit.At(1)))
	i := pullFile(run)
	i.Source.Properties = map[string][]string{"quality": {"selected"}}
	retry := kernel.Clone(i)
	storekit.Get[model.PullItem](t)(p.CreateItem(ctx, i))
	i.Source.Properties["quality"][0] = "caller-mutated"
	i = storekit.Get[model.PullItem](t)(p.Item(ctx, run.Reference, i.Action, i.Path))
	if i.Source.Properties["quality"][0] != "selected" {
		t.Fatal("retained source aliases caller properties")
	}
	next := run
	next.Version++
	next.SourceDone, next.Phase = true, model.PullReconcile
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, next) }), kernel.ErrConflict)
	settled := kernel.Clone(i)
	settled.Version++
	settled.Phase, settled.Outcome, settled.Reason = model.PullItemComplete, model.PullSkipped, "source_changed"
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, i.Version, settled) }))
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, i.Version, settled) }), kernel.ErrConflict)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, next) }))
	run = next
	_, err := p.CreateItem(ctx, retry)
	storekit.Must(t, err) // a retry keeps its original refusal even after the phase closes
	late := pullFile(run)
	late.Path, late.Source.Path = "selected/late", "selected/late"
	_, err = p.CreateItem(ctx, late)
	storekit.WantErr(t, err, kernel.ErrConflict)
	d := model.PullItem{
		Run: run.Reference, Action: model.PullDelete, Path: "selected/stale", Version: 1, Phase: model.PullIntent, Updated: run.Updated,
		Target: &artifactsapi.PullCacheCandidate{Repository: binding.Source, Selection: binding.Selection(), Path: "selected/stale", Version: strings.Repeat("e", 64)},
	}
	storekit.Get[model.PullItem](t)(p.CreateItem(ctx, d))
	next = run
	next.Version++
	next.TargetDone, next.Phase, next.Completed = true, model.PullComplete, next.Updated
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, next) }), kernel.ErrConflict)
	done := d
	done.Version++
	done.Phase, done.Outcome = model.PullItemComplete, model.PullRetained
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, d.Version, done) }))
	storekit.Same(t, "retention survives recovery without fake removal paths", done, storekit.Get[model.PullItem](t)(p.Item(ctx, d.Run, d.Action, d.Path)))
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, next) }))
	_, err = p.CreateItem(ctx, d)
	storekit.WantErr(t, err, kernel.ErrConflict)
	storekit.Must(t, s.Replications().Put(ctx, model.Replication{Key: "target", RepoKey: "remote", Revision: "target-revision"}))
	storekit.Must(t, s.Replications().Delete(ctx, "target"))
	storekit.Same(t, "target removal retains settled provenance", next, storekit.Get[model.PullRun](t)(p.Run(ctx, next.Reference)))
}

func pullPages(t *testing.T, s port.PullStore) {
	ctx, p := t.Context(), s.PullProgress()
	run := storekit.Get[model.PullRun](t)(p.Create(ctx, "run", pullBinding(), storekit.At(1)))
	foreign := storekit.Get[model.PullRun](t)(p.Create(ctx, "foreign", pullBinding(), storekit.At(1)))
	storekit.Get[model.PullItem](t)(p.CreateItem(ctx, pullFile(foreign)))
	keys := make([]string, 0, 41)
	rows := map[string]model.PullItem{}
	for n := range 41 {
		i := pullFile(run)
		i.Path = fmt.Sprintf("selected/%02d", n)
		i.Source.Path, i.Source.UpstreamPath = i.Path, i.Path
		i = storekit.Get[model.PullItem](t)(p.CreateItem(ctx, i))
		key := storekit.Get[string](t)(model.PullItemKey(i.Action, i.Path))
		keys, rows[key] = append(keys, key), i
	}
	slices.Sort(keys)
	for _, key := range keys[:20] {
		i := rows[key]
		i.Version++
		i.Phase, i.Outcome, i.Reason = model.PullItemComplete, model.PullSkipped, "source_changed"
		storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, 1, i) }))
	}
	for _, pending := range []bool{false, true} {
		expected := keys
		if pending {
			expected = keys[20:]
		}
		page := kernel.Page{Limit: 3}
		var got []string
		for {
			items, next, err := p.Items(ctx, run.Reference, pending, page)
			storekit.Must(t, err)
			if len(items) != min(page.Limit, len(expected)-len(got)) {
				t.Fatal("pending eligibility applied after page limit")
			}
			for _, i := range items {
				if i.Run != run.Reference {
					t.Fatal("foreign intent consumed a page")
				}
				got = append(got, storekit.Get[string](t)(model.PullItemKey(i.Action, i.Path)))
			}
			if next == "" {
				break
			}
			page.After = next
		}
		storekit.Same(t, "bounded recovery has no omissions or repeats", expected, got)
	}
}

var errPullRollback = errors.New("reject final pull execution fence")

func pullRollback(t *testing.T, s port.PullStore) {
	ctx, p := t.Context(), s.PullProgress()
	var ref model.PullReference
	err := s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Create(ctx, "rollback", pullBinding(), storekit.At(1))
		if err != nil {
			return fmt.Errorf("create rollback pull run: %w", err)
		}
		ref = run.Reference
		if _, err = p.CreateItem(ctx, pullFile(run)); err != nil {
			return fmt.Errorf("create rollback pull intent: %w", err)
		}
		return errPullRollback
	})
	storekit.WantErr(t, err, errPullRollback)
	_, err = p.Run(ctx, ref)
	storekit.WantErr(t, err, kernel.ErrNotFound)
}
