package storetest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

var errStorageOwnershipRollback = errors.New("rollback ownership")

// RunStorageUsage verifies canonical ownership, logical duplicate content,
// raw/materialized cache accounting and restart initialization inputs.
func RunStorageUsage(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("ownership and retained cache bodies", func(t *testing.T) {
		storageOwnership(t, newStore(t))
	})
	t.Run("invalid totals fail closed", func(t *testing.T) {
		for _, sizes := range [][]int64{{-1, 2}, {math.MaxInt64, 1}} {
			st := newStore(t)
			storekit.Must(t, st.Repos().Create(t.Context(), repo.Config{Key: "invalid", Class: repo.Local, ProjectKey: "alpha"}))
			for i, size := range sizes {
				storekit.Get[model.Item](t)(st.Items().Put(t.Context(), model.Item{RepoKey: "invalid", Name: strings.Repeat("x", i+1), Size: size}))
			}
			usage := st.Repos().(port.StorageUsage)
			if _, err := usage.ProjectStorageBytes(t.Context(), "alpha"); err == nil {
				t.Fatal("invalid project byte total was accepted", sizes)
			}
			if _, err := usage.RepositoryStorageBytes(t.Context(), "invalid"); err == nil {
				t.Fatal("invalid repository byte total was accepted", sizes)
			}
		}
	})
}

func storageOwnership(t *testing.T, st port.Store) {
	t.Helper()
	ctx := t.Context()
	usage, ok := st.Repos().(port.StorageUsage)
	if !ok {
		t.Fatal("canonical logical-storage capability is unavailable")
	}
	local := repo.Config{Key: "local", Class: repo.Local, ProjectKey: "alpha"}
	for _, c := range []repo.Config{local, {Key: "remote", Class: repo.Remote, ProjectKey: "alpha"}, {Key: "foreign", Class: repo.Local, ProjectKey: "beta"}, {Key: "unassigned", Class: repo.Local}} {
		storekit.Must(t, st.Repos().Create(ctx, c))
	}
	put := func(repository, name string, size int64, cached, folder bool) model.Item {
		return storekit.Get[model.Item](t)(st.Items().Put(ctx, model.Item{
			RepoKey: repository, Name: name, Size: size, Cached: cached, Folder: folder,
			Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)},
		}))
	}
	put("local", "one.pkg", 11, false, false)
	put("local", "same-content.pkg", 11, false, false)
	put("local", "folder", 100, false, true)
	storekit.Get[model.Item](t)(st.Items().EnsureRoot(ctx, model.RootItem("local", false)))
	materialized := put("remote", "cache.pkg", 5, true, false)
	put("foreign", "foreign.pkg", 43, false, false)
	put("unassigned", "unassigned.pkg", 17, false, false)
	cache := st.RemoteCache()
	putCache := func(path string, size, itemID int64, digest string) model.RemoteCacheEntry {
		token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, model.RemoteCacheKey{RepoKey: "remote", Path: path}))
		return storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, model.RemoteCacheEntry{
			Token: token, Kind: model.RemoteMetadata, ItemID: itemID,
			Blob: blob.Blob{Size: size, Checksums: kernel.Checksums{SHA256: digest}},
		}))
	}
	raw := putCache("raw.json", 7, 0, strings.Repeat("b", 64))
	putCache("cache.pkg", 5, materialized.ID, materialized.Actual.SHA256)
	putCache("no-body.json", 999, 0, "")
	storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, model.RemoteCacheKey{RepoKey: "remote", Path: "tombstone"}))
	storekit.Must(t, cache.Negative(ctx, raw.Token, storekit.At(10)))
	assertProjectBytes(t, ctx, usage, "alpha", 34)
	assertProjectBytes(t, ctx, usage, "beta", 43)
	assertProjectBytes(t, ctx, usage, "", 17)
	assertProjectBytes(t, ctx, usage, "missing", 0)
	if got, err := usage.RepositoryStorageBytes(ctx, "remote"); err != nil || got != 12 {
		t.Fatal("raw positive body behind negative or materialized cache was miscounted", got, err)
	}
	local.ProjectKey = "beta"
	storekit.Must(t, st.Repos().Update(ctx, local))
	assertProjectBytes(t, ctx, usage, "alpha", 12)
	assertProjectBytes(t, ctx, usage, "beta", 65)
	err := st.Tx(ctx, func(ctx context.Context) error {
		changed := local
		changed.ProjectKey = "alpha"
		if err := st.Repos().Update(ctx, changed); err != nil {
			return fmt.Errorf("transfer repository in rollback fixture: %w", err)
		}
		assertProjectBytes(t, ctx, usage, "alpha", 34)
		assertProjectBytes(t, ctx, usage, "beta", 43)
		return errStorageOwnershipRollback
	})
	storekit.WantErr(t, err, errStorageOwnershipRollback)
	assertProjectBytes(t, ctx, usage, "alpha", 12)
	assertProjectBytes(t, ctx, usage, "beta", 65)
	storekit.Must(t, st.Repos().Delete(ctx, local.Key))
	assertProjectBytes(t, ctx, usage, "beta", 43)
	if got, err := usage.RepositoryStorageBytes(ctx, local.Key); err != nil || got != 22 {
		t.Fatal("configuration deletion hid retained item rows from reconciliation", got, err)
	}
	local.ProjectKey = "alpha"
	storekit.Must(t, st.Repos().Create(ctx, local))
	assertProjectBytes(t, ctx, usage, "alpha", 34)
	current := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, raw.Token.Key))
	storekit.Must(t, cache.Evict(ctx, current.Token))
	assertProjectBytes(t, ctx, usage, "alpha", 27)
}

func assertProjectBytes(t *testing.T, ctx context.Context, usage port.StorageUsage, key string, want int64) {
	t.Helper()
	if got, err := usage.ProjectStorageBytes(ctx, key); err != nil || got != want {
		t.Fatalf("project %q logical bytes = %d, want %d: %v", key, got, want, err)
	}
}
