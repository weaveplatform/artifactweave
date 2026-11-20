package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

var errStorageChangeRefused = errors.New("storage change fixture refusal")

type storageChangeWatch struct {
	t                       *testing.T
	store                   port.Store
	prepareFail, recordFail bool
	deltas                  []int64
}

func (w *storageChangeWatch) Content(ctx context.Context, _ string) (api.StorageChange, error) {
	w.t.Helper()
	if !w.store.(interface{ InTx(context.Context) bool }).InTx(ctx) {
		return nil, fmt.Errorf("storage preparation outside mutation transaction: %w", kernel.ErrStore)
	}
	if w.prepareFail {
		return nil, errStorageChangeRefused
	}
	return func(ctx context.Context, delta int64) error {
		// A fixture-owned blob-index row is a transactional counter witness;
		// it is not the production project counter or a physical blob upload.
		var err error
		if delta < 0 {
			_, err = w.store.Blobs().Unref(ctx, storageChangeWitness().Checksums.SHA256, -delta)
		} else {
			_, err = w.store.Blobs().Ref(ctx, storageChangeWitness(), delta)
		}
		if err != nil {
			return fmt.Errorf("write transactional storage witness: %w", err)
		}
		w.deltas = append(w.deltas, delta)
		if w.recordFail {
			return errStorageChangeRefused
		}
		return nil
	}, nil
}

func (*storageChangeWatch) Ownership(context.Context, api.StorageOwnership) (api.StorageChange, error) {
	return nil, nil //nolint:nilnil // this fixture has no ownership accounting
}

func storageChangeWitness() blob.Blob {
	return blob.Blob{Checksums: kernel.Checksums{SHA256: strings.Repeat("3", 64)}}
}

// RunStorageChanges proves the optional composition watch sees actual item
// deltas and that refusing either stage rolls metadata and its handoff back.
func RunStorageChanges(t *testing.T, newStore func(*testing.T, api.StorageWatch) port.Store) {
	t.Helper()
	watch := &storageChangeWatch{t: t}
	st := newStore(t, watch)
	watch.store = st
	ctx := t.Context()
	storekit.Must(t, st.Repos().Create(ctx, repo.Config{Key: "items", Class: repo.Local}))
	one := storekit.Get[model.Item](t)(st.Items().Put(ctx, model.Item{RepoKey: "items", Name: "one", Size: 7}))
	one.Size = 3
	one = storekit.Get[model.Item](t)(st.Items().Put(ctx, one))
	storekit.Get[model.Item](t)(st.Items().Put(ctx, model.Item{RepoKey: "items", Name: "cached", Cached: true, Size: 2}))
	storekit.Get[model.Item](t)(st.Items().Put(ctx, model.Item{RepoKey: "items", Name: "folder", Folder: true, Size: 99}))
	storekit.Get[model.Item](t)(st.Items().EnsureRoot(ctx, model.RootItem("items", false)))
	if !slices.Equal(watch.deltas, []int64{7, -4, 2, 0}) {
		t.Fatal("content hook did not receive actual logical changes", watch.deltas)
	}
	assertStorageWitness(t, st, 5)
	_, err := st.Items().Create(ctx, one)
	storekit.WantErr(t, err, kernel.ErrConflict)
	assertStorageWitness(t, st, 5)
	watch.prepareFail = true
	_, err = st.Items().Put(ctx, model.Item{RepoKey: "items", Name: "prepare-refused", Size: 8})
	storekit.WantErr(t, err, errStorageChangeRefused)
	watch.prepareFail = false
	watch.recordFail = true
	_, err = st.Items().Put(ctx, model.Item{RepoKey: "items", Name: "record-refused", Size: 8})
	storekit.WantErr(t, err, errStorageChangeRefused)
	for _, name := range []string{"prepare-refused", "record-refused"} {
		_, err := st.Items().Get(ctx, "items", false, name)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	}
	assertStorageWitness(t, st, 5)
	_, err = st.Items().Delete(ctx, "items", false, one.FullPath())
	storekit.WantErr(t, err, errStorageChangeRefused)
	if retained := storekit.Get[model.Item](t)(st.Items().GetByID(ctx, one.ID)); retained.Size != 3 {
		t.Fatal("refused deletion changed the actual item", retained)
	}
	assertStorageWitness(t, st, 5)
	err = st.Tx(ctx, func(ctx context.Context) error {
		_, _ = st.Items().Put(ctx, model.Item{RepoKey: "items", Name: "ignored-refusal", Size: 8})
		return nil
	})
	storekit.WantErr(t, err, errStorageChangeRefused)
	_, err = st.Items().Get(ctx, "items", false, "ignored-refusal")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	assertStorageWitness(t, st, 5)
	watch.recordFail = false
	removed := storekit.Get[port.DeleteResult](t)(st.Items().Delete(ctx, "items", false, "."))
	if removed.Bytes != 3 {
		t.Fatal("deletion accounted for folder metadata or cached rows", removed.Bytes)
	}
	assertStorageWitness(t, st, 2)
	storekit.Get[port.DeleteResult](t)(st.Items().Delete(ctx, "items", true, "."))
	assertStorageWitness(t, st, 0)
	storekit.Get[model.Item](t)(st.Items().Root(ctx, "items", false))
}

func assertStorageWitness(t *testing.T, st port.Store, want int64) {
	t.Helper()
	row := storekit.Get[blob.Blob](t)(st.Blobs().Get(t.Context(), storageChangeWitness().Checksums.SHA256))
	if row.Refs != want {
		t.Fatalf("transactional storage witness = %d, want %d", row.Refs, want)
	}
}
