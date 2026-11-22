package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func executionOptions(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	binding := executionBinding("")
	binding.OneShot = true
	run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "run:with/slashes", binding, storekit.At(1)))
	options := model.ExecutionOptions{ // #nosec G101 -- fixture-only password, never a deployed credential
		URL: "https://peer.invalid/artifactory/destination", Username: "receiver", Password: "lab-only-one-shot",
		SyncProperties: true, Include: "selected/**", SocketTimeout: time.Second,
	}
	storekit.WantErr(t, progress.PutOptions(ctx, run.Reference, options), kernel.ErrInvalid)
	_, err := progress.Options(ctx, run.Reference)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	err = db.Tx(ctx, func(ctx context.Context) error {
		if err := progress.PutOptions(ctx, run.Reference, options); err != nil {
			return fmt.Errorf("create rollback options fixture: %w", err)
		}
		return errExecutionRollback
	})
	storekit.WantErr(t, err, errExecutionRollback)
	_, err = progress.Options(ctx, run.Reference)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	put := func(value model.ExecutionOptions) error {
		return db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, value) })
	}
	storekit.Must(t, put(options))
	storekit.Must(t, put(options))
	storekit.Same(t, "resolved options survive replay", options, storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, run.Reference)))
	changed := options
	changed.Password = "different-lab-secret"
	storekit.WantErr(t, put(changed), kernel.ErrConflict)
	changed = options
	changed.SyncDeletes = true
	storekit.WantErr(t, put(changed), kernel.ErrConflict)
	wrong := run.Reference
	wrong.Incarnation += "recreated"
	_, err = progress.Options(ctx, wrong)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, wrong, options) }), kernel.ErrNotFound)
	ordinary := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "stored-run", executionBinding(""), storekit.At(2)))
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, ordinary.Reference, options) }), kernel.ErrConflict)
}
