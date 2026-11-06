package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runBuildRowLock covers the row locks of build runs, which only a
// transaction may take.
func runBuildRowLock(t *testing.T, factory Factory) {
	t.Run("BuildRowLock", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		b := get[types.Build](
			t,
		)(
			s.Builds().Put(ctx, types.Build{Name: "lock", Number: "1", Started: base}),
		)
		_, err := s.Builds().GetForUpdate(ctx, "", b.Name, b.Number, base)
		wantErr(t, err, store.ErrInvalid)
		_, err = s.Builds().ByIDForUpdate(ctx, b.ID)
		wantErr(t, err, store.ErrInvalid)
		must(t, s.Tx(ctx, func(ctx context.Context) error {
			got, err := s.Builds().GetForUpdate(ctx, "", b.Name, b.Number, base)
			if err == nil && got.ID != b.ID {
				t.Errorf("locked run %d, want %d", got.ID, b.ID)
			}
			if err != nil {
				return err
			}
			got, err = s.Builds().ByIDForUpdate(ctx, b.ID)
			if err == nil && got.ID != b.ID {
				t.Errorf("locked run by ID %d, want %d", got.ID, b.ID)
			}
			return err
		}))
	})
}
