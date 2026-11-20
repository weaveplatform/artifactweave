package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunProjectStorage exercises initialization, durable warning identity,
// rollback and project-lifetime isolation on each real metadata backend.
func RunProjectStorage(t *testing.T, newStore Factory) {
	t.Helper()
	s := newStore(t)
	ps, ctx := s.Projects(), t.Context()
	createProjects(t, s, "alpha", "beta")
	read := func(key string, want model.ProjectStorage, initialized bool) {
		t.Helper()
		got, found, err := ps.Storage(ctx, key)
		storekit.Must(t, err)
		if got != want || found != initialized {
			t.Fatalf("%s storage=%+v,%v; want %+v,%v", key, got, found, want, initialized)
		}
	}
	read("alpha", model.ProjectStorage{}, false)
	storekit.Must(t, ps.SetStorage(ctx, "alpha", model.ProjectStorage{}))
	read("alpha", model.ProjectStorage{}, true)
	first := model.ProjectStorage{Bytes: 75, WarningKey: "stable-warning-occurrence"}
	storekit.Must(t, ps.SetStorage(ctx, "alpha", first))
	read("alpha", first, true)
	read("beta", model.ProjectStorage{}, false)
	storekit.Must(t, ps.Update(ctx, model.Project{Key: "alpha", DisplayName: "lowered quota", StorageQuotaBytes: 50}))
	read("alpha", first, true)
	rollback := errors.New("abort accounting publication") //nolint:err113 // matched by identity
	err := s.Tx(ctx, func(ctx context.Context) error {
		if err := ps.Guard(ctx); err != nil {
			return err //nolint:wrapcheck // the store owns this error
		}
		if err := ps.SetStorage(ctx, "alpha", model.ProjectStorage{Bytes: 76, WarningKey: "uncommitted"}); err != nil {
			return err //nolint:wrapcheck // the store owns this error
		}
		if err := ps.SetStorage(ctx, "beta", model.ProjectStorage{Bytes: 7}); err != nil {
			return err //nolint:wrapcheck // the store owns this error
		}
		return rollback
	})
	storekit.WantErr(t, err, rollback)
	read("alpha", first, true)
	read("beta", model.ProjectStorage{}, false)
	storekit.WantErr(t, ps.SetStorage(ctx, "alpha", model.ProjectStorage{Bytes: -1}), kernel.ErrInvalid)
	read("alpha", first, true)
	storekit.Must(t, ps.Delete(ctx, "alpha"))
	_, found, err := ps.Storage(ctx, "alpha")
	if found || !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("deleted project retained accounting", found, err)
	}
	storekit.WantErr(t, ps.SetStorage(ctx, "alpha", first), kernel.ErrNotFound)
	storekit.Must(t, ps.Create(ctx, project("alpha")))
	read("alpha", model.ProjectStorage{}, false)
	storekit.Must(t, ps.SetStorage(ctx, "beta", model.ProjectStorage{Bytes: 17}))
	read("beta", model.ProjectStorage{Bytes: 17}, true)
}
