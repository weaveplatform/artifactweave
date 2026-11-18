package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func runNPMExternals(t *testing.T, newStore Factory) {
	t.Run("NPMExternals", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		c := repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.NPM}
		storekit.Must(t, s.Repos().Create(ctx, c))
		row := model.NPMExternal{Virtual: c.Key, Name: "@scope/dep", Spec: "first-art-external", URL: "https://example/first.tgz", File: "first.tgz", SHA256: "digest"}
		storekit.Must(t, s.NPMExternals().Put(ctx, row))
		got, err := s.NPMExternals().Get(ctx, c.Key, row.Name, row.Spec)
		storekit.Must(t, err)
		storekit.Same(t, "durable binding", got, row)
		blank := row
		blank.SHA256 = ""
		storekit.Must(t, s.NPMExternals().Put(ctx, blank))
		got, err = s.NPMExternals().Get(ctx, c.Key, row.Name, row.Spec)
		storekit.Must(t, err)
		storekit.Same(t, "render preserves byte identity", got, row)
		replaced := row
		replaced.URL = "https://other/first.tgz"
		storekit.WantErr(t, s.NPMExternals().Put(ctx, replaced), kernel.ErrConflict)
		replaced = row
		replaced.File = "other.tgz"
		storekit.WantErr(t, s.NPMExternals().Put(ctx, replaced), kernel.ErrConflict)
		rows, err := s.NPMExternals().List(ctx, c.Key, row.Name)
		storekit.Must(t, err)
		if len(rows) != 1 || rows[0] != row {
			t.Fatal(rows)
		}
		rows, err = s.NPMExternals().List(ctx, c.Key, "other")
		storekit.Must(t, err)
		if len(rows) != 0 {
			t.Fatal(rows)
		}
		_, err = s.NPMExternals().Get(ctx, c.Key, "other", row.Spec)
		storekit.WantErr(t, err, kernel.ErrNotFound)
		storekit.WantErr(t, s.NPMExternals().Put(ctx, model.NPMExternal{}), kernel.ErrInvalid)
		absent := row
		absent.Virtual = "absent"
		if err = s.NPMExternals().Put(ctx, absent); err == nil {
			t.Fatal("absent virtual accepted")
		}
		rollback := errors.New("rollback npm external binding") //nolint:err113 // identity is the rollback contract
		err = s.Tx(ctx, func(ctx context.Context) error {
			r := row
			r.Spec = "rollback"
			if err := s.NPMExternals().Put(ctx, r); err != nil {
				return err //nolint:wrapcheck // the transaction returns the store contract error
			}
			return rollback
		})
		storekit.WantErr(t, err, rollback)
		_, err = s.NPMExternals().Get(ctx, c.Key, row.Name, "rollback")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		storekit.Must(t, s.Repos().Delete(ctx, c.Key))
		storekit.Must(t, s.Repos().Create(ctx, c))
		_, err = s.NPMExternals().Get(ctx, c.Key, row.Name, row.Spec)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	})
}
