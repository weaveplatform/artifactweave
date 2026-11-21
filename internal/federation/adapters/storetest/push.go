package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func runPushProgress(t *testing.T, newStore Factory) {
	t.Run("PushProgress", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			run  func(*testing.T, port.PushStore)
		}{
			{"immutable runs", pushRuns},
			{"item recovery and traversal", pushItems},
			{"bounded isolated pages", pushPages},
			{"rollback", pushRollback},
			{"competing CAS", pushConcurrent},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store, ok := newStore(t).(port.PushStore)
				if !ok {
					t.Fatal("federation store has no durable push progress")
				}
				tc.run(t, store)
			})
		}
	})
}

func pushBinding() model.PushBinding {
	return model.PushBinding{
		TargetKey: "target", TargetRevision: "target-revision", TransportRevision: "transport-revision",
		Source:     artifactsapi.TransferRepository{Key: "source", Incarnation: "source-lifetime", Generation: "source-revision", PackageType: repo.Generic},
		Credential: kernel.CredentialReference{Name: "reader", TokenID: "source-token-id"},
		Root:       "selected", Include: "**", SyncDeletes: true, SyncProperties: true,
	}
}

func pushRun(t *testing.T, s port.PushStore, id string) model.PushRun {
	t.Helper()
	return storekit.Get[model.PushRun](t)(s.PushProgress().Create(t.Context(), id, pushBinding(), storekit.At(1)))
}

func pushUpload(r model.PushRun, path string) model.PushItem {
	return model.PushItem{
		Run: r.Reference, Action: model.PushUpload, Path: path, Version: 1, Phase: model.PushIntent, Updated: r.Started,
		Source: &artifactsapi.TransferItem{
			Repository: r.Binding.Source, ID: 7, Path: path, Size: 6,
			Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Version: strings.Repeat("b", 64),
			PropertiesIncluded: true, Properties: map[string][]string{"quality": {"verified"}},
		},
	}
}

func pushRuns(t *testing.T, s port.PushStore) {
	ctx, progress := t.Context(), s.PushProgress()
	r := pushRun(t, s, "run")
	storekit.Same(t, "run", r, storekit.Get[model.PushRun](t)(progress.Run(ctx, r.Reference)))
	storekit.Same(t, "creation retry", r, storekit.Get[model.PushRun](t)(progress.Create(ctx, "run", pushBinding(), storekit.At(9))))
	other := r.Binding
	other.TargetRevision = "replacement"
	_, err := progress.Create(ctx, "run", other, storekit.At(9))
	storekit.WantErr(t, err, kernel.ErrConflict)
	stale := r.Reference
	stale.Incarnation = "another-lifetime"
	_, err = progress.Run(ctx, stale)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	next := r
	next.Version++
	next.SourceCursor, next.Updated = "continued", storekit.At(2)
	storekit.WantErr(t, progress.Advance(ctx, r.Version, next), kernel.ErrInvalid)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, r.Version, next) }))
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, r.Version, next) }), kernel.ErrConflict)
	storekit.Same(t, "retry keeps progress", next, storekit.Get[model.PushRun](t)(progress.Create(ctx, "run", pushBinding(), storekit.At(9))))
	// Public target deletion must not erase evidence and silently make the old
	// logical ID available to a new private revision.
	storekit.Must(t, s.Replications().Put(ctx, model.Replication{Key: "target", RepoKey: "source", Revision: "target-revision"}))
	storekit.Must(t, s.Replications().Delete(ctx, "target"))
	storekit.Same(t, "retained provenance", next, storekit.Get[model.PushRun](t)(progress.Run(ctx, r.Reference)))
	_, err = progress.Create(ctx, "run", other, storekit.At(9))
	storekit.WantErr(t, err, kernel.ErrConflict)
}

func pushAdvanceRun(t *testing.T, s port.PushStore, current *model.PushRun, edit func(*model.PushRun)) {
	t.Helper()
	next := kernel.Clone(*current)
	next.Version++
	next.Updated = next.Updated.Add(time.Second)
	edit(&next)
	storekit.Must(t, s.Tx(t.Context(), func(ctx context.Context) error {
		return s.PushProgress().Advance(ctx, current.Version, next)
	}))
	*current = next
}

func pushAdvanceItem(t *testing.T, s port.PushStore, current *model.PushItem, edit func(*model.PushItem)) {
	t.Helper()
	next := kernel.Clone(*current)
	next.Version++
	next.Updated = next.Updated.Add(time.Second)
	edit(&next)
	storekit.Must(t, s.Tx(t.Context(), func(ctx context.Context) error {
		return s.PushProgress().AdvanceItem(ctx, current.Version, next)
	}))
	*current = next
}

func pushItems(t *testing.T, s port.PushStore) {
	ctx, progress := t.Context(), s.PushProgress()
	r := pushRun(t, s, "run")
	intent := pushUpload(r, "selected/item.pkg")
	i := storekit.Get[model.PushItem](t)(progress.CreateItem(ctx, intent))
	storekit.Same(t, "creation retry", i, storekit.Get[model.PushItem](t)(progress.CreateItem(ctx, intent)))
	changed := kernel.Clone(intent)
	changed.Source.Properties["quality"][0] = "different"
	_, err := progress.CreateItem(ctx, changed)
	storekit.WantErr(t, err, kernel.ErrConflict)
	// Returning or retaining a snapshot must not expose the stored maps.
	i.Source.Properties["quality"][0] = "caller-owned"
	i = storekit.Get[model.PushItem](t)(progress.Item(ctx, r.Reference, i.Action, i.Path))
	if i.Source.Properties["quality"][0] != "verified" {
		t.Fatal("stored source property map aliases caller data")
	}
	next := kernel.Clone(i)
	next.Version++
	next.Phase, next.VerifiedTarget = model.PushContentVerified, &model.PushTargetSnapshot{Actual: i.Source.Actual, Size: i.Source.Size}
	storekit.WantErr(t, progress.AdvanceItem(ctx, i.Version, next), kernel.ErrInvalid)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return progress.AdvanceItem(ctx, i.Version, next) }))
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return progress.AdvanceItem(ctx, i.Version, next) }), kernel.ErrConflict)
	i = next
	storekit.Same(t, "retry retains verified content", i, storekit.Get[model.PushItem](t)(progress.CreateItem(ctx, intent)))
	pushAdvanceRun(t, s, &r, func(r *model.PushRun) { r.SourceDone = true })
	late := pushUpload(r, "selected/late.pkg")
	_, err = progress.CreateItem(ctx, late)
	storekit.WantErr(t, err, kernel.ErrConflict)
	blocked := r
	blocked.Version++
	blocked.Phase = model.PushReconcile
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, r.Version, blocked) }), kernel.ErrConflict)
	pushAdvanceItem(t, s, &i, func(i *model.PushItem) { i.Phase = model.PushMetadataVerified })
	pushAdvanceItem(t, s, &i, func(i *model.PushItem) { i.Phase, i.Outcome = model.PushItemComplete, model.PushSucceeded })
	pushAdvanceRun(t, s, &r, func(r *model.PushRun) { r.Phase = model.PushReconcile })
	deletion := model.PushItem{
		Run: r.Reference, Action: model.PushDelete, Path: "selected/stale.pkg", Version: 1, Phase: model.PushIntent, Updated: r.Updated,
		Target: &model.PushTargetSnapshot{Actual: kernel.Checksums{SHA256: strings.Repeat("c", 64)}, Size: 8},
	}
	d := storekit.Get[model.PushItem](t)(progress.CreateItem(ctx, deletion))
	pushAdvanceRun(t, s, &r, func(r *model.PushRun) { r.TargetDone = true })
	blocked = r
	blocked.Version++
	blocked.Phase, blocked.Completed = model.PushComplete, blocked.Updated
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, r.Version, blocked) }), kernel.ErrConflict)
	pushAdvanceItem(t, s, &d, func(i *model.PushItem) {
		i.Phase, i.Outcome, i.TargetAbsent = model.PushItemComplete, model.PushSucceeded, true
	})
	pushAdvanceRun(t, s, &r, func(r *model.PushRun) { r.Phase, r.Completed = model.PushComplete, r.Updated })
	_, err = progress.CreateItem(ctx, intent)
	storekit.WantErr(t, err, kernel.ErrConflict)
	rows, nextPage, err := progress.Items(ctx, r.Reference, true, kernel.Page{Limit: 1})
	storekit.Must(t, err)
	if len(rows) != 0 || nextPage != "" {
		t.Fatal("completed run retained pending intents", rows, nextPage)
	}
}

func pushPages(t *testing.T, s port.PushStore) {
	ctx, progress := t.Context(), s.PushProgress()
	r := pushRun(t, s, "run")
	foreign := pushRun(t, s, "foreign")
	_, err := progress.CreateItem(ctx, pushUpload(foreign, "selected/foreign.pkg"))
	storekit.Must(t, err)
	wanted := make([]string, 0, 41)
	intents := make(map[string]model.PushItem, 41)
	for n := range 41 {
		i := pushUpload(r, fmt.Sprintf("selected/%02d.pkg", n))
		created, err := progress.CreateItem(ctx, i)
		storekit.Must(t, err)
		key := storekit.Get[string](t)(model.PushItemKey(i.Action, i.Path))
		wanted = append(wanted, key)
		intents[key] = created
	}
	slices.Sort(wanted)
	// Put completed rows before every pending row in traversal order. Applying
	// the pending filter after LIMIT would return an empty first recovery page.
	completed := len(wanted) / 2
	for _, key := range wanted[:completed] {
		i := intents[key]
		pushAdvanceItem(t, s, &i, func(i *model.PushItem) {
			i.Phase, i.Outcome, i.Reason = model.PushItemComplete, model.PushSkipped, "source_changed"
		})
	}
	for _, pending := range []bool{true, false} {
		expected := wanted
		if pending {
			expected = wanted[completed:]
		}
		page := kernel.Page{Limit: 3}
		var got []string
		for {
			rows, next, err := progress.Items(ctx, r.Reference, pending, page)
			storekit.Must(t, err)
			if len(rows) != min(page.Limit, len(expected)-len(got)) {
				t.Fatal("push page limit applied before eligibility", pending, len(rows), len(got))
			}
			for _, row := range rows {
				if row.Run != r.Reference {
					t.Fatal("foreign run consumed a recovery page")
				}
				got = append(got, storekit.Get[string](t)(model.PushItemKey(row.Action, row.Path)))
			}
			if next == "" {
				break
			}
			page.After = next
		}
		if !slices.Equal(got, expected) {
			t.Fatal("push recovery skipped or repeated an intent", pending, got, expected)
		}
	}
	_, _, err = progress.Items(ctx, r.Reference, true, kernel.Page{After: "%%%"})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
}

var errPushRollback = errors.New("push checkpoint rollback")

func pushRollback(t *testing.T, s port.PushStore) {
	ctx, progress := t.Context(), s.PushProgress()
	var ref model.PushReference
	err := s.Tx(ctx, func(ctx context.Context) error {
		r, err := progress.Create(ctx, "rolled-back", pushBinding(), storekit.At(1))
		if err != nil {
			return fmt.Errorf("create rollback push run: %w", err)
		}
		ref = r.Reference
		if _, err := progress.CreateItem(ctx, pushUpload(r, "selected/item.pkg")); err != nil {
			return fmt.Errorf("create rollback push intent: %w", err)
		}
		return errPushRollback
	})
	storekit.WantErr(t, err, errPushRollback)
	_, err = progress.Run(ctx, ref)
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

func pushConcurrent(t *testing.T, s port.PushStore) {
	ctx, progress := t.Context(), s.PushProgress()
	r := pushRun(t, s, "run")
	results := make(chan error, 2)
	for _, cursor := range []string{"first", "second"} {
		next := r
		next.Version++
		next.SourceCursor = cursor
		go func() {
			results <- s.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, r.Version, next) })
		}()
	}
	var accepted, conflicted int
	var unexpected []error
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, kernel.ErrConflict):
			conflicted++
		default:
			unexpected = append(unexpected, err)
		}
	}
	if len(unexpected) > 0 {
		t.Fatal("competing progress returned a misleading error", unexpected)
	}
	if accepted != 1 || conflicted != 1 {
		t.Fatal("both workers published the same progress version", accepted, conflicted)
	}
}
