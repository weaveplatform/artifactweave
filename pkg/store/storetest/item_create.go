package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func testItemCreate(t *testing.T, st store.Store) {
	ctx := t.Context()
	items := st.Items()
	original := fileItem("r", "archive/package.tar.gz", 1)
	created := get[types.Item](t)(items.Create(ctx, original))
	same(t, "created item", stored(original, created.ID), created)
	props := types.Properties{"owner": {"original"}}
	must(t, items.SetProperties(ctx, created.ID, props, time.Now()))
	for _, replacement := range []types.Item{fileItem("r", original.FullPath(), 2), folderItem("r", original.FullPath())} {
		_, err := items.Create(ctx, replacement)
		wantErr(t, err, store.ErrConflict)
	}
	same(
		t,
		"original bytes",
		original.Actual,
		get[types.Item](t)(items.Get(ctx, "r", false, original.FullPath())).Actual,
	)
	same(
		t,
		"original properties",
		props,
		get[types.Properties](t)(items.Properties(ctx, created.ID)),
	)
	_, err := items.Create(ctx, types.Item{Name: "invalid"})
	wantErr(t, err, store.ErrInvalid)
	cached := original
	cached.Cached = true
	get[types.Item](t)(items.Create(ctx, cached))
	rollback := errors.New("rollback")
	err = st.Tx(ctx, func(ctx context.Context) error {
		_, err := items.Create(ctx, fileItem("r", "rolled-back", 3))
		if err != nil {
			return err
		}
		return rollback
	})
	wantErr(t, err, rollback)
	_, err = items.Get(ctx, "r", false, "rolled-back")
	wantErr(t, err, store.ErrNotFound)

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
			wantErr(t, err, store.ErrConflict)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d items at one path", createdCount)
	}
}
