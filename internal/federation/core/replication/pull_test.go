package replication_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
)

type pullWorld struct {
	*world
	driver            replication.Pull
	platform          *platforminmem.Store
	source            artifactsapi.PullRepository
	items             []artifactsapi.PullItem
	targets           []artifactsapi.PullCacheCandidate
	fetchError        error
	beforeFetch       func()
	omitRecord        bool
	retained          map[string]bool
	pages, fetches    int
	deletes, restores int
}

type pullSource struct{ w *pullWorld }

func newPullWorld(t *testing.T, deletes bool) (*pullWorld, context.Context, model.PullRun) {
	t.Helper()
	w := &pullWorld{world: newWorld()}
	w.platform = platforminmem.New(w.db.Store)
	ctx := accessapi.WithCaller(t.Context(), kernel.Principal{Name: "original", TokenID: "verified-token"}, nil)
	must(t, w.service.Replace(ctx, "remote", replication.Input{Enabled: ptr(true), SyncProperties: ptr(false), SyncDeletes: ptr(deletes)}))
	w.source = artifactsapi.PullRepository{Repository: artifactsapi.TransferRepository{
		Key: "remote", Incarnation: "remote-lifetime", Generation: "generation", PackageType: repo.Generic,
	}, Revision: strings.Repeat("a", 64)}
	w.driver = replication.Pull{Store: w.db, Configurations: w.service, Source: pullSource{w}, Now: func() time.Time { return time.Unix(100, 0) }}
	w.driver.Restore = func(ctx context.Context, reference kernel.CredentialReference) (context.Context, error) {
		if w.db.InTx(ctx) || reference.Name != "original" || reference.TokenID != "verified-token" {
			t.Fatal("pull borrowed a metadata transaction or another caller")
		}
		w.restores++
		return accessapi.WithCaller(ctx, reference.Principal(), nil), nil
	}
	target := stored(t, w.world, "remote")
	run, err := w.driver.Start(ctx, replication.PullRequest{ID: "logical-pull", TargetKey: target.Key, TargetRevision: target.Revision, Root: "selected"}, nil)
	must(t, err)
	return w, ctx, run
}

func (w *pullWorld) add(run model.PullRun, path string) {
	w.items = append(w.items, artifactsapi.PullItem{
		Repository: w.source, Selection: run.Binding.Selection(), Path: path, UpstreamPath: path,
		Size: 4, Actual: pushSums("body"), Version: strings.Repeat("b", 64),
	})
}

func (s pullSource) Repository(ctx context.Context, _ string, _ artifactsapi.PullSelection) (artifactsapi.PullRepository, error) {
	if !s.w.db.InTx(ctx) {
		return artifactsapi.PullRepository{}, fmt.Errorf("source binding escaped the authority transaction: %w", kernel.ErrInvalid)
	}
	return s.w.source, nil
}

func (s pullSource) Items(ctx context.Context, _ artifactsapi.PullRepository, _ artifactsapi.PullSelection, page kernel.Page) ([]artifactsapi.PullItem, string, error) {
	if s.w.db.InTx(ctx) || page.Limit != 1 {
		return nil, "", kernel.ErrInvalid
	}
	s.w.pages++
	for index, item := range s.w.items {
		if item.Path > page.After {
			next := ""
			if index+1 < len(s.w.items) {
				next = item.Path
			}
			return []artifactsapi.PullItem{item}, next, nil
		}
	}
	return nil, "", nil
}

func (s pullSource) Fetch(ctx context.Context, item artifactsapi.PullItem, publication artifactsapi.PullPublication) (artifactsapi.PullCached, error) {
	if s.w.db.InTx(ctx) {
		return artifactsapi.PullCached{}, kernel.ErrInvalid
	}
	s.w.fetches++
	if s.w.beforeFetch != nil {
		s.w.beforeFetch()
	}
	if err := publication.Check(ctx); err != nil {
		return artifactsapi.PullCached{}, err
	}
	if s.w.fetchError != nil {
		return artifactsapi.PullCached{}, s.w.fetchError
	}
	receipt := artifactsapi.PullCached{Item: item, Version: strings.Repeat("c", 64), Representation: strings.Repeat("d", 64)}
	err := s.w.db.Tx(ctx, func(ctx context.Context) error {
		if err := publication.Check(ctx); err != nil {
			return err
		}
		if err := s.w.platform.Settings().Put(ctx, "cache-effect:"+item.Path, true); err != nil {
			return err
		}
		if !s.w.omitRecord {
			if err := publication.Record(ctx, receipt); err != nil {
				return err
			}
		}
		return publication.Fence(ctx)
	})
	return receipt, err
}

func (s pullSource) WithCurrent(context.Context, artifactsapi.PullCached, func(context.Context) error) error {
	return kernel.ErrInvalid // driver completion must use the owner's atomic Record, never a later proof
}

func (s pullSource) CachedItems(
	ctx context.Context,
	_ artifactsapi.PullRepository,
	_ artifactsapi.PullSelection,
	page kernel.Page,
) ([]artifactsapi.PullCacheCandidate, string, error) {
	if s.w.db.InTx(ctx) || page.Limit != 1 {
		return nil, "", kernel.ErrInvalid
	}
	for index, candidate := range s.w.targets {
		if candidate.Path > page.After {
			next := ""
			if index+1 < len(s.w.targets) {
				next = candidate.Path
			}
			return []artifactsapi.PullCacheCandidate{candidate}, next, nil
		}
	}
	return nil, "", nil
}

func (s pullSource) Remove(ctx context.Context, candidate artifactsapi.PullCacheCandidate, publication artifactsapi.PullPublication,
	record func(context.Context, []string) error,
) ([]string, error) {
	if s.w.db.InTx(ctx) {
		return nil, kernel.ErrInvalid
	}
	s.w.deletes++
	paths := []string{candidate.Path, candidate.Path + ".sha256"}
	if s.w.retained[candidate.Path] {
		paths = nil
	}
	err := s.w.db.Tx(ctx, func(ctx context.Context) error {
		if err := publication.Check(ctx); err != nil {
			return err
		}
		if err := record(ctx, paths); err != nil {
			return err
		}
		return publication.Fence(ctx)
	})
	return paths, err
}

func pullFence(context.Context) error { return nil }

func finishPull(t *testing.T, w *pullWorld, run model.PullRun) model.PullRun {
	t.Helper()
	for step := 0; step < 20 && !run.Phase.Terminal(); step++ {
		var err error
		run, err = w.driver.Step(t.Context(), run.Reference, pullFence)
		must(t, err)
	}
	if run.Phase != model.PullComplete {
		t.Fatal("bounded pull did not settle")
	}
	return run
}

func TestUnitPullRecoversPendingIntentBeforeAdvancingEnumeration(t *testing.T) {
	w, _, run := newPullWorld(t, false)
	w.add(run, "selected/a")
	w.add(run, "selected/b")
	run, err := w.driver.Step(t.Context(), run.Reference, pullFence)
	must(t, err)
	if w.fetches != 0 || w.pages != 1 {
		t.Fatal("enumeration performed an unrecorded fetch")
	}
	w.fetchError = artifactsapi.ErrPullTemporary
	if _, err := w.driver.Step(t.Context(), run.Reference, pullFence); !errors.Is(err, artifactsapi.ErrPullTemporary) {
		t.Fatal("temporary peer failure lost retry identity", err)
	}
	w.fetchError = nil
	run = finishPull(t, w, run)
	items, next, err := w.db.PullProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 10})
	must(t, err)
	if next != "" || len(items) != 2 || w.pages != 2 || w.fetches != 3 {
		t.Fatal("retry advanced past pending intent or duplicated successful effects")
	}
	for _, item := range items {
		var retained bool
		must(t, w.platform.Settings().Get(t.Context(), "cache-effect:"+item.Path, &retained))
		if item.Outcome != model.PullSucceeded || item.Cache == nil || !retained {
			t.Fatal("successful intent has no matching committed cache state")
		}
	}
	fetches := w.fetches
	_, err = w.driver.Step(t.Context(), run.Reference, pullFence)
	must(t, err)
	if w.fetches != fetches {
		t.Fatal("terminal run repeated network work")
	}
}

func TestUnitPullFinalLeaseLossRollsBackCacheEffectAndReceipt(t *testing.T) {
	w, _, run := newPullWorld(t, false)
	w.add(run, "selected/a")
	_, err := w.driver.Step(t.Context(), run.Reference, pullFence)
	must(t, err)
	calls := 0
	fence := func(context.Context) error {
		calls++
		if calls > 1 {
			return kernel.ErrConflict
		}
		return nil
	}
	if _, err := w.driver.Step(t.Context(), run.Reference, fence); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("expired worker published successful pull", err)
	}
	var retained bool
	if err := w.platform.Settings().Get(t.Context(), "cache-effect:selected/a", &retained); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("cache effect survived lost final lease", err)
	}
	item, err := w.db.PullProgress().Item(t.Context(), run.Reference, model.PullFetch, "selected/a")
	must(t, err)
	if item.Phase != model.PullIntent || item.Cache != nil || item.Outcome != "" {
		t.Fatal("lost lease left terminal evidence")
	}
	finishPull(t, w, run)
}

func TestUnitPullClassifiesOnlyVerifiedPermanentPerItemFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		outcome model.PullOutcome
	}{
		{"changed", artifactsapi.ErrPullChanged, model.PullSkipped},
		{"integrity", artifactsapi.ErrPullIntegrity, model.PullItemFailed},
		{"not-stored", artifactsapi.ErrPullNotStored, model.PullItemFailed},
		{"unsupported", artifactsapi.ErrPullUnsupported, model.PullItemFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, _, run := newPullWorld(t, false)
			w.add(run, "selected/a")
			w.fetchError = test.failure
			run = finishPull(t, w, run)
			item, err := w.db.PullProgress().Item(t.Context(), run.Reference, model.PullFetch, "selected/a")
			must(t, err)
			if item.Outcome != test.outcome || item.Cache != nil || item.Reason == "" {
				t.Fatal("failed or superseded item was called a cached success")
			}
		})
	}
}

func TestUnitPullRechecksSourceLifetimeBeforeCommittingBlockedFetch(t *testing.T) {
	w, _, run := newPullWorld(t, false)
	w.add(run, "selected/a")
	_, err := w.driver.Step(t.Context(), run.Reference, pullFence)
	must(t, err)
	w.beforeFetch = func() { w.source.Revision = strings.Repeat("e", 64) }
	if _, err := w.driver.Step(t.Context(), run.Reference, pullFence); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("old source/options binding committed after an update", err)
	}
	item, err := w.db.PullProgress().Item(t.Context(), run.Reference, model.PullFetch, "selected/a")
	must(t, err)
	if item.Phase != model.PullIntent || item.Cache != nil {
		t.Fatal("changed source lifetime retained completion authority")
	}
}

func TestUnitPullReconciliationRecordsExactOwnerRemovalPaths(t *testing.T) {
	w, _, run := newPullWorld(t, true)
	w.targets = []artifactsapi.PullCacheCandidate{{Repository: w.source, Selection: run.Binding.Selection(), Path: "selected/gone", Version: strings.Repeat("f", 64)}}
	run = finishPull(t, w, run)
	item, err := w.db.PullProgress().Item(t.Context(), run.Reference, model.PullDelete, "selected/gone")
	must(t, err)
	if w.fetches != 0 || w.deletes != 1 || item.Outcome != model.PullSucceeded || !slices.Equal(item.Removed, []string{"selected/gone", "selected/gone.sha256"}) {
		t.Fatal("reconciliation lost actual cache removal/companion evidence")
	}
}

func TestUnitPullMultiFileReconciliationSeparatesRetentionFromRemovalAndRefusal(t *testing.T) {
	w, _, run := newPullWorld(t, true)
	w.add(run, "selected/a")
	w.add(run, "selected/b")
	w.retained = map[string]bool{"selected/a": true, "selected/b": true}
	for _, path := range []string{"selected/a", "selected/b", "selected/gone"} {
		w.targets = append(w.targets, artifactsapi.PullCacheCandidate{Repository: w.source, Selection: run.Binding.Selection(), Path: path, Version: strings.Repeat("f", 64)})
	}
	run = finishPull(t, w, run)
	if w.fetches != 2 || w.deletes != 3 {
		t.Fatal("multi-item reconciliation omitted selected work")
	}
	for _, path := range []string{"selected/a", "selected/b"} {
		i, err := w.db.PullProgress().Item(t.Context(), run.Reference, model.PullDelete, path)
		must(t, err)
		if i.Outcome != model.PullOutcome("retained") || i.Reason != "" || len(i.Removed) != 0 || i.Cache != nil || i.Validate(run) != nil {
			t.Fatal("normal upstream presence became a refusal or removal success", i.Outcome, i.Reason)
		}
	}
	removed, err := w.db.PullProgress().Item(t.Context(), run.Reference, model.PullDelete, "selected/gone")
	must(t, err)
	if removed.Outcome != model.PullSucceeded || len(removed.Removed) != 2 {
		t.Fatal("real absence lost exact removal evidence")
	}
}

func TestUnitPullRefusesOmittedReceiptWithoutCallingItSuccess(t *testing.T) {
	w, _, run := newPullWorld(t, false)
	w.add(run, "selected/a")
	_, err := w.driver.Step(t.Context(), run.Reference, pullFence)
	must(t, err)
	w.omitRecord = true
	if _, err := w.driver.Step(t.Context(), run.Reference, pullFence); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("owner return without atomic receipt counted as success", err)
	}
	item, err := w.db.PullProgress().Item(t.Context(), run.Reference, model.PullFetch, "selected/a")
	must(t, err)
	if item.Phase != model.PullIntent || item.Cache != nil {
		t.Fatal("missing owner checkpoint manufactured completion evidence")
	}
}

func TestUnitPullCancellationAndMissingFenceCannotReachEnumeration(t *testing.T) {
	w, _, run := newPullWorld(t, false)
	if _, err := w.driver.Step(t.Context(), run.Reference, nil); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("pull accepted missing exclusive publication fence", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := w.driver.Step(ctx, run.Reference, pullFence); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled pull performed another step", err)
	}
	if w.pages != 0 || w.fetches != 0 || w.restores != 0 {
		t.Fatal("cancelled or unfenced pull reached authority or network work")
	}
}
