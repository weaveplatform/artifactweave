package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

// runBuildRowLock covers the row locks of build runs, which only a
// transaction may take.
func runBuildRowLock(t *testing.T, factory Factory) {
	t.Run("BuildRowLock", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		b := storekit.Get[model.Build](
			t,
		)(
			s.Builds().Put(ctx, model.Build{Name: "lock", Number: "1", Started: storekit.Base}),
		)
		_, err := s.Builds().GetForUpdate(ctx, "", b.Name, b.Number, storekit.Base)
		storekit.WantErr(t, err, kernel.ErrInvalid)
		_, err = s.Builds().ByIDForUpdate(ctx, b.ID)
		storekit.WantErr(t, err, kernel.ErrInvalid)
		storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
			got, err := s.Builds().GetForUpdate(ctx, "", b.Name, b.Number, storekit.Base)
			if err == nil && got.ID != b.ID {
				t.Errorf("locked run %d, want %d", got.ID, b.ID)
			}
			if err != nil {
				return err //nolint:wrapcheck // the transaction returns the store's own error
			}
			got, err = s.Builds().ByIDForUpdate(ctx, b.ID)
			if err == nil && got.ID != b.ID {
				t.Errorf("locked run by ID %d, want %d", got.ID, b.ID)
			}
			return err //nolint:wrapcheck // the transaction returns the store's own error
		}))
	})
}
