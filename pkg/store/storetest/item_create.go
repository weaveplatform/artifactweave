package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func testItemCreate(t *testing.T, st store.Store) {
	ctx := t.Context()
	items := st.Items()
	original := fileItem("r", "archive/package.tar.gz", 1)
	created := storekit.Get[types.Item](t)(items.Create(ctx, original))
	storekit.Same(t, "created item", stored(original, created.ID), created)
	props := types.Properties{"owner": {"original"}}
	storekit.Must(t, items.SetProperties(ctx, created.ID, props, time.Now()))
	for _, replacement := range []types.Item{fileItem("r", original.FullPath(), 2), folderItem("r", original.FullPath())} {
		_, err := items.Create(ctx, replacement)
		storekit.WantErr(t, err, store.ErrConflict)
	}
	storekit.Same(
		t,
		"original bytes",
		original.Actual,
		storekit.Get[types.Item](t)(items.Get(ctx, "r", false, original.FullPath())).Actual,
	)
	storekit.Same(
		t,
		"original properties",
		props,
		storekit.Get[types.Properties](t)(items.Properties(ctx, created.ID)),
	)
	_, err := items.Create(ctx, types.Item{Name: "invalid"})
	storekit.WantErr(t, err, store.ErrInvalid)
	cached := original
	cached.Cached = true
	storekit.Get[types.Item](t)(items.Create(ctx, cached))
	rollback := errors.New("rollback")
	err = st.Tx(ctx, func(ctx context.Context) error {
		_, err := items.Create(ctx, fileItem("r", "rolled-back", 3))
		if err != nil {
			return err
		}
		return rollback
	})
	storekit.WantErr(t, err, rollback)
	_, err = items.Get(ctx, "r", false, "rolled-back")
	storekit.WantErr(t, err, store.ErrNotFound)

	// Two writers racing for a fresh path cannot replace each other's bytes.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n := range 2 {
		wg.Go(func() {
			_, err := items.Create(ctx, fileItem("r", "raced", n+10))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	createdCount := 0
	for err := range results {
		if err == nil {
			createdCount++
		} else {
			storekit.WantErr(t, err, store.ErrConflict)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d items at one path", createdCount)
	}
}
