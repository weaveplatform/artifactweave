package pull_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

func selectedFile(t *testing.T, w *world) api.PullItem {
	t.Helper()
	w.peer.entries = []pull.Entry{file("a")}
	repository := binding(t, w, api.PullSelection{})
	items, _, err := w.source.Items(t.Context(), repository, api.PullSelection{}, kernel.Page{})
	if err != nil || len(items) != 1 {
		t.Fatal("selected fixture", err)
	}
	return items[0]
}

func publication() api.PullPublication {
	return api.PullPublication{Check: func(context.Context) error { return nil }, Fence: func(context.Context) error { return nil }}
}

func retainedFile(t *testing.T, w *world, selected api.PullItem) model.RemoteCacheEntry {
	t.Helper()
	it, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: selected.Repository.Repository.Key, Cached: true, Name: selected.Path,
		Size: selected.Size, Actual: selected.Actual,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := model.RemoteCacheKey{RepoKey: it.RepoKey, Path: selected.Path, SettingsRevision: "current-options"}
	token, err := w.db.RemoteCache().Begin(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := w.db.RemoteCache().Put(t.Context(), token, model.RemoteCacheEntry{
		Kind: model.RemoteUnique, ItemID: it.ID, Blob: blob.Blob{Size: selected.Size, Checksums: selected.Actual},
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestUnitPullFetchCannotReportTransientOrForeignCacheStateAsSuccess(t *testing.T) {
	for _, mode := range []string{"transient", "foreign-source", "incomplete-body", "changed-peer", "revoked-completion"} {
		t.Run(mode, func(t *testing.T) {
			w := fixture(t)
			selected := selectedFile(t, w)
			entry := retainedFile(t, w, selected)
			grant := publication()
			current := true
			grant.Check = func(context.Context) error {
				if !current {
					return kernel.ErrForbidden
				}
				return nil
			}
			w.source.Read = func(ctx context.Context, request source.Request) (source.Result, error) {
				if w.db.InTx(ctx) || request.Resource.SelectedContent == nil || request.Publication == nil ||
					request.Resource.SelectedContent.Checksums.SHA256 != selected.Actual.SHA256 {
					t.Fatal("cache fill bypassed the external selected P9 contract")
				}
				if mode == "transient" {
					return source.Result{Stream: &source.Stream{Body: io.NopCloser(strings.NewReader("data"))}}, nil
				}
				if mode == "foreign-source" {
					entry.Token.Source.Generation = "foreign-generation"
				}
				if mode == "incomplete-body" {
					entry.Blob.Size--
				}
				if mode == "changed-peer" {
					w.peer.binding = "changed-options"
				}
				if mode == "revoked-completion" {
					current = false
				}
				return source.Result{CacheEntry: &entry}, nil
			}
			proof, err := w.source.Fetch(t.Context(), selected, grant)
			if err == nil || proof.Version != "" || proof.Representation != "" {
				t.Fatal("uncommitted/stale cache facts escaped as a completed pull", err)
			}
		})
	}
}

func TestUnitPullCacheCheckpointIsAtomicAndCannotReuseEvictedRepresentation(t *testing.T) {
	w := fixture(t)
	selected := selectedFile(t, w)
	entry := retainedFile(t, w, selected)
	w.source.Read = func(context.Context, source.Request) (source.Result, error) {
		return source.Result{CacheEntry: &entry}, nil
	}
	proof, err := w.source.Fetch(t.Context(), selected, publication())
	if err != nil {
		t.Fatal(err)
	}
	err = w.source.WithCurrent(t.Context(), proof, func(ctx context.Context) error {
		if !w.db.InTx(ctx) {
			t.Fatal("checkpoint escaped its transaction")
		}
		if err := w.db.Settings().Put(ctx, "pull-checkpoint", true); err != nil {
			return err
		}
		return kernel.ErrConflict
	})
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("failed worker fence was hidden", err)
	}
	var value bool
	if err := w.db.Settings().Get(t.Context(), "pull-checkpoint", &value); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("checkpoint survived its rejected final fence", err)
	}
	if _, err := w.db.RemoteCache().Invalidate(t.Context(), "remote", "a", false); err != nil {
		t.Fatal(err)
	}
	called := false
	err = w.source.WithCurrent(t.Context(), proof, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, source.ErrSuperseded) || called {
		t.Fatal("evicted content retained successful checkpoint authority", err)
	}
}

func TestUnitPullFreshPolicyWarmReceiptKeepsBytesAndRefreshesCASAuthority(t *testing.T) {
	w := fixture(t)
	old := selectedFile(t, w)
	entry := retainedFile(t, w, old)
	w.config.RetrievalCachePeriodSecs = new(12345)
	if err := w.db.Repos().Update(t.Context(), w.config); err != nil {
		t.Fatal(err)
	}
	selected := selectedFile(t, w)
	w.source.Read = func(context.Context, source.Request) (source.Result, error) {
		return source.Result{CacheEntry: &entry}, nil
	}
	proof, err := w.source.Fetch(t.Context(), selected, publication())
	if err != nil {
		t.Fatal("fresh pull rejected verified warm bytes after policy edit", err)
	}
	if err := w.source.WithCurrent(t.Context(), proof, func(context.Context) error { return nil }); err != nil {
		t.Fatal("renewed warm receipt did not describe committed cache authority", err)
	}
	if _, err := w.source.Fetch(t.Context(), old, publication()); !errors.Is(err, source.ErrSuperseded) {
		t.Fatal("old selected intent survived policy edit", err)
	}
}

func TestUnitPullFetchRefusesTransactionsAndIncompleteExecutionAuthority(t *testing.T) {
	w := fixture(t)
	selected := selectedFile(t, w)
	reads := 0
	w.source.Read = func(context.Context, source.Request) (source.Result, error) { reads++; return source.Result{}, nil }
	if err := w.db.Tx(t.Context(), func(ctx context.Context) error {
		_, err := w.source.Fetch(ctx, selected, publication())
		return err
	}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("transactional fetch reached the network boundary", err)
	}
	grant := publication()
	grant.Fence = nil
	if _, err := w.source.Fetch(t.Context(), selected, grant); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("fetch accepted an absent ownership fence", err)
	}
	selected.Size++
	if _, err := w.source.Fetch(t.Context(), selected, publication()); !errors.Is(err, source.ErrInvalidRequest) || reads != 0 {
		t.Fatal("altered selected facts reached P9", err)
	}
}

func TestUnitPullFreshPolicyPropertySyncRenewsEverySharedRepresentation(t *testing.T) {
	w := fixture(t)
	selection := api.PullSelection{Properties: true}
	selectFile := func() api.PullItem {
		t.Helper()
		w.peer.entries = []pull.Entry{file("a")}
		w.peer.props = model.Properties{"quality": {"upstream"}}
		repository := binding(t, w, selection)
		items, _, err := w.source.Items(t.Context(), repository, selection, kernel.Page{})
		if err != nil || len(items) != 1 {
			t.Fatal(err)
		}
		return items[0]
	}
	selected := selectFile()
	entry := retainedFile(t, w, selected)
	key := entry.Token.Key
	key.Variant = "same-item-query-variant"
	token, err := w.db.RemoteCache().Begin(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.RemoteCache().Put(t.Context(), token, entry); err != nil {
		t.Fatal(err)
	}
	w.config.RetrievalCachePeriodSecs = new(12345)
	if err := w.db.Repos().Update(t.Context(), w.config); err != nil {
		t.Fatal(err)
	}
	selected = selectFile()
	w.source.Read = func(context.Context, source.Request) (source.Result, error) {
		return source.Result{CacheEntry: &entry}, nil
	}
	proof, err := w.source.Fetch(t.Context(), selected, publication())
	if err != nil {
		t.Fatal("fresh property pull rejected a shared warm representation", err)
	}
	if err := w.source.WithCurrent(t.Context(), proof, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, key := range []model.RemoteCacheKey{entry.Token.Key, key} {
		current, err := w.db.RemoteCache().Read(t.Context(), key)
		if err != nil || current.Smart == nil || len(current.Smart.OwnedProperties["quality"]) != 1 {
			t.Fatal("shared representation lost remote property ownership", err)
		}
	}
}

// A file-list entry has no MD5 or lastUpdated, so the fetch's own inspection
// is compared with it only on the facts both reads carry (#1895).
func TestUnitPullFetchComparesListedFactsOnTheFactsBothReadsCarry(t *testing.T) {
	for _, mode := range []string{"unchanged", "digest", "size", "modified", "properties-updated"} {
		t.Run(mode, func(t *testing.T) {
			w := fixture(t)
			selection := api.PullSelection{Properties: mode == "properties-updated"}
			listed := file("a")
			listed.Listed, listed.Checksums.SHA1 = true, strings.Repeat("1", 40)
			info := file("a")
			info.Checksums.SHA1, info.Checksums.MD5, info.Updated = strings.Repeat("1", 40), strings.Repeat("5", 32), time.Unix(30, 0)
			w.peer.entries, w.peer.inspected = []pull.Entry{listed}, &info
			repository := binding(t, w, selection)
			items, _, err := w.source.Items(t.Context(), repository, selection, kernel.Page{})
			if err != nil || len(items) != 1 {
				t.Fatal("listed selection", err)
			}
			selected := items[0]
			entry := retainedFile(t, w, selected)
			w.source.Read = func(context.Context, source.Request) (source.Result, error) {
				return source.Result{CacheEntry: &entry}, nil
			}
			changed := info
			switch mode {
			case "digest":
				changed.Checksums.SHA1 = strings.Repeat("2", 40)
			case "size":
				changed.Size++
			case "modified":
				changed.Modified = changed.Modified.Add(time.Second)
			case "properties-updated":
				changed.Updated = changed.Updated.Add(time.Second)
			}
			w.peer.inspected = &changed
			_, err = w.source.Fetch(t.Context(), selected, publication())
			if mode == "unchanged" && err != nil {
				t.Fatal("facts the list does not carry superseded an unchanged file", err)
			}
			if mode != "unchanged" && !errors.Is(err, source.ErrSuperseded) {
				t.Fatal("a changed peer file kept its listed intent", err)
			}
		})
	}
}
