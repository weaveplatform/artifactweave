package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func testItemCreate(t *testing.T, st port.Store) {
	ctx := t.Context()
	items := st.Items()
	original := fileItem("r", "archive/package.tar.gz", 1)
	created := storekit.Get[model.Item](t)(items.Create(ctx, original))
	storekit.Same(t, "created item", stored(original, created.ID), created)
	props := model.Properties{"owner": {"original"}}
	storekit.Must(t, items.SetProperties(ctx, created.ID, props, time.Now()))
	for _, replacement := range []model.Item{fileItem("r", original.FullPath(), 2), folderItem("r", original.FullPath())} {
		_, err := items.Create(ctx, replacement)
		storekit.WantErr(t, err, kernel.ErrConflict)
	}
	storekit.Same(
		t,
		"original bytes",
		original.Actual,
		storekit.Get[model.Item](t)(items.Get(ctx, "r", false, original.FullPath())).Actual,
	)
	storekit.Same(
		t,
		"original properties",
		props,
		storekit.Get[model.Properties](t)(items.Properties(ctx, created.ID)),
	)
	_, err := items.Create(ctx, model.Item{Name: "invalid"})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	cached := original
	cached.Cached = true
	storekit.Get[model.Item](t)(items.Create(ctx, cached))
	rollback := errors.New("rollback") //nolint:err113 // the test matches it by identity
	err = st.Tx(ctx, func(ctx context.Context) error {
		_, err := items.Create(ctx, fileItem("r", "rolled-back", 3))
		if err != nil {
			return err //nolint:wrapcheck // the transaction returns the store's own error
		}
		return rollback
	})
	storekit.WantErr(t, err, rollback)
	_, err = items.Get(ctx, "r", false, "rolled-back")
	storekit.WantErr(t, err, kernel.ErrNotFound)

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
			storekit.WantErr(t, err, kernel.ErrConflict)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d items at one path", createdCount)
	}
}
