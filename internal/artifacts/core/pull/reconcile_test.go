package pull_test

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
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
	for _, mode := range []string{"filtered-virtual-omission", "assumed-offline", "revoked", "replaced", "new-representation", "changed-options"} {
		t.Run(mode, func(t *testing.T) {
			w, entry, candidate := removalFixture(t)
			switch mode {
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

func TestUnitPullPresentSourceSettlesRetentionWithCurrentAuthorityAndFinalFence(t *testing.T) {
	for _, mode := range []string{"commit", "lost-lease", "changed-cache", "revoked", "delete-revoked"} {
		t.Run(mode, func(t *testing.T) {
			w, entry, candidate := removalFixture(t)
			w.peer.absent = false
			canDelete := true
			open := w.source.Open
			w.source.Open = func(ctx context.Context, key, root string) (*source.Service, source.Request, error) {
				svc, q, err := open(ctx, key, root)
				q.Access = &accessapitest.Decider{Admin: true, AllowsFunc: func(_ accessapi.ResourceType, _ accessapi.Repo, _ string, want accessapi.Actions) bool {
					return canDelete || want&accessapi.ActionDelete == 0
				}}
				return svc, q, err
			}
			w.peer.afterAbsence = func() {
				if w.db.InTx(t.Context()) {
					t.Fatal("upstream observation occurred in a metadata transaction")
				}
				if mode == "revoked" {
					w.openErr = kernel.ErrForbidden
				}
				if mode == "delete-revoked" {
					canDelete = false
				}
				if mode == "changed-cache" {
					if _, err := w.db.RemoteCache().Put(t.Context(), entry.Token, entry); err != nil {
						t.Fatal(err)
					}
				}
			}
			grant := publication()
			fences := 0
			grant.Fence = func(ctx context.Context) error {
				if !w.db.InTx(ctx) {
					t.Fatal("retained observation escaped its final transaction")
				}
				fences++
				if mode == "lost-lease" {
					return kernel.ErrConflict
				}
				return nil
			}
			paths, err := w.source.Remove(t.Context(), candidate, grant, func(ctx context.Context, paths []string) error {
				if !w.db.InTx(ctx) || len(paths) != 0 {
					t.Fatal("retention was claimed as an actual deletion")
				}
				return w.db.Settings().Put(ctx, "retained-cache-checkpoint", true)
			})
			var retained bool
			checkpointErr := w.db.Settings().Get(t.Context(), "retained-cache-checkpoint", &retained)
			if mode == "commit" {
				if err != nil || len(paths) != 0 || fences != 1 || checkpointErr != nil || !retained {
					t.Fatal("healthy upstream presence became a warning or fake removal", err, checkpointErr)
				}
			} else if err == nil || len(paths) != 0 || !errors.Is(checkpointErr, kernel.ErrNotFound) {
				t.Fatal("stale or unfenced retention committed evidence", err, checkpointErr)
			}
			if _, err := w.db.Items().Get(t.Context(), "remote", true, candidate.Path); err != nil {
				t.Fatal("retention removed cache bytes", err)
			}
			if _, err := w.db.RemoteCache().Read(t.Context(), entry.Token.Key); err != nil {
				t.Fatal("retention invalidated the cache representation", err)
			}
		})
	}
}

func TestUnitPullFreshReconciliationRetiresEarlierSourceAndPolicyEntries(t *testing.T) {
	for _, change := range []string{"generation", "policy"} {
		t.Run(change, func(t *testing.T) {
			w, entry, old := removalFixture(t)
			if change == "generation" {
				w.config.URL = "https://replacement.example/artifactory/source"
			} else {
				w.config.RetrievalCachePeriodSecs = new(12345)
			}
			if err := w.db.Repos().Update(t.Context(), w.config); err != nil {
				t.Fatal(err)
			}
			repository := binding(t, w, old.Selection)
			candidates, _, err := w.source.CachedItems(t.Context(), repository, old.Selection, kernel.Page{})
			if err != nil || len(candidates) != 1 {
				t.Fatal("fresh reconciliation could not select retained earlier cache", err)
			}
			removed, err := w.source.Remove(t.Context(), candidates[0], publication(), nil)
			if err != nil || len(removed) != 1 || removed[0] != "a" {
				t.Fatal("fresh current absence could not retire earlier cache", removed, err)
			}
			current, err := w.db.RemoteCache().Read(t.Context(), entry.Token.Key)
			if !errors.Is(err, kernel.ErrNotFound) || current.Blob.Checksums.SHA256 != "" {
				t.Fatal("retired cache representation survived", err)
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
