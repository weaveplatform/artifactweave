package pull_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

func removalFixture(t *testing.T) (*world, model.RemoteCacheEntry, api.PullCacheCandidate) {
	t.Helper()
	w := fixture(t)
	selected := selectedFile(t, w)
	entry := retainedFile(t, w, selected)
	if _, err := w.db.Blobs().Ref(t.Context(), blob.Blob{Size: selected.Size, Checksums: selected.Actual}, 1); err != nil {
		t.Fatal(err)
	}
	items, next, err := w.source.CachedItems(t.Context(), selected.Repository, selected.Selection, kernel.Page{})
	if err != nil || len(items) != 1 || next != "" {
		t.Fatal("cache reconciliation fixture", err)
	}
	w.peer.absent = true
	return w, entry, items[0]
}

func TestUnitPullCacheRemovalNeedsExactAbsenceAndCurrentRepresentation(t *testing.T) {
	for _, mode := range []string{"present", "filtered-virtual-omission", "assumed-offline", "revoked", "replaced", "new-representation", "changed-options"} {
		t.Run(mode, func(t *testing.T) {
			w, entry, candidate := removalFixture(t)
			switch mode {
			case "present":
				w.peer.absent = false
			case "filtered-virtual-omission":
				w.peer.absentErr = source.ErrUnsupported
			case "assumed-offline":
				w.peer.absentErr = source.ErrAssumedOffline
			case "revoked":
				w.openErr = kernel.ErrForbidden
			case "replaced":
				if _, err := w.db.RemoteCache().Put(t.Context(), entry.Token, entry); err != nil {
					t.Fatal(err)
				}
			case "new-representation":
				key := entry.Token.Key
				key.Variant = "another-native-representation"
				token, err := w.db.RemoteCache().Begin(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.db.RemoteCache().Put(t.Context(), token, entry); err != nil {
					t.Fatal(err)
				}
			case "changed-options":
				w.peer.binding = "new-options"
			}
			called := false
			paths, err := w.source.Remove(t.Context(), candidate, publication(), func(context.Context, []string) error { called = true; return nil })
			if err == nil || len(paths) != 0 || called {
				t.Fatal("unproven absence or stale authority became a deletion", err)
			}
			if _, err := w.db.Items().Get(t.Context(), "remote", true, "a"); err != nil {
				t.Fatal("refused reconciliation changed the cache item", err)
			}
		})
	}
}

func TestUnitPullExactRemovalPreservesRawDescendantRepresentations(t *testing.T) {
	w, _, candidate := removalFixture(t)
	key := model.RemoteCacheKey{RepoKey: "remote", Path: "a/metadata", Variant: "raw-native", SettingsRevision: "current-options"}
	token, err := w.db.RemoteCache().Begin(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := w.db.RemoteCache().Put(t.Context(), token, model.RemoteCacheEntry{
		Kind: model.RemoteMetadata, Blob: blob.Blob{Size: 3, Checksums: kernel.Checksums{SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.source.Remove(t.Context(), candidate, publication(), nil); err != nil {
		t.Fatal(err)
	}
	current, err := w.db.RemoteCache().Read(t.Context(), key)
	if err != nil || current.Token != entry.Token || current.Blob.Checksums.SHA256 != entry.Blob.Checksums.SHA256 {
		t.Fatal("exact cache reconciliation recursively invalidated another representation", err)
	}
}

func TestUnitPullCacheRemovalAndCheckpointRollBackTogether(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "lost-lease"}[refuse], func(t *testing.T) {
			w, entry, candidate := removalFixture(t)
			grant := publication()
			grant.Fence = func(ctx context.Context) error {
				if !w.db.InTx(ctx) {
					t.Fatal("removal fence escaped transaction")
				}
				if refuse {
					return kernel.ErrConflict
				}
				return nil
			}
			paths, err := w.source.Remove(t.Context(), candidate, grant, func(ctx context.Context, paths []string) error {
				if len(paths) != 1 || paths[0] != "a" {
					t.Fatal("checkpoint did not receive actual removed paths")
				}
				if _, err := w.db.Items().Get(ctx, "remote", true, "a"); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("checkpoint preceded cache item deletion", err)
				}
				return w.db.Settings().Put(ctx, "cache-removal-checkpoint", true)
			})
			var checkpoint bool
			checkErr := w.db.Settings().Get(t.Context(), "cache-removal-checkpoint", &checkpoint)
			_, itemErr := w.db.Items().Get(t.Context(), "remote", true, "a")
			current, cacheErr := w.db.RemoteCache().Read(t.Context(), entry.Token.Key)
			if refuse {
				if !errors.Is(err, kernel.ErrConflict) || len(paths) != 0 || itemErr != nil || cacheErr != nil ||
					current.Token != entry.Token || !errors.Is(checkErr, kernel.ErrNotFound) {
					t.Fatal("lost ownership left deletion/checkpoint effects", err, itemErr, cacheErr, checkErr)
				}
			} else if err != nil || len(paths) != 1 || !errors.Is(itemErr, kernel.ErrNotFound) ||
				checkErr != nil || !checkpoint || cacheErr == nil && current.Blob.Checksums.SHA256 != "" {
				t.Fatal("verified deletion did not commit with its checkpoint", err, itemErr, cacheErr, checkErr)
			}
		})
	}
}

func TestUnitPullFetchRecordsItsVerifiedReceiptBeforeFinalFence(t *testing.T) {
	w := fixture(t)
	selected := selectedFile(t, w)
	entry := retainedFile(t, w, selected)
	w.source.Read = func(context.Context, source.Request) (source.Result, error) {
		return source.Result{CacheEntry: &entry}, nil
	}
	grant := publication()
	grant.Record = func(ctx context.Context, proof api.PullCached) error {
		if !w.db.InTx(ctx) || proof.Version == "" || proof.Item.Version != selected.Version {
			t.Fatal("receipt checkpoint was not transactional and verified")
		}
		return w.db.Settings().Put(ctx, "verified-pull-receipt", true)
	}
	grant.Fence = func(context.Context) error { return kernel.ErrConflict }
	if _, err := w.source.Fetch(t.Context(), selected, grant); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("failed final fence was hidden", err)
	}
	var saved bool
	if err := w.db.Settings().Get(t.Context(), "verified-pull-receipt", &saved); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("failed worker completed its receipt checkpoint", err)
	}
}
