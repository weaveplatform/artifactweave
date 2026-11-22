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
	next := run
	next.Version++
	next.Phase, next.Updated = model.ExecutionRunning, storekit.At(3)
	storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, run.Version, next) }))
	storekit.Must(t, put(options))
	storekit.Same(t, "progress permits exact options replay", options, storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, run.Reference)))
	storekit.Same(t, "options replay preserves progressed run", next, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	changed := options
	changed.Password = "different-lab-secret"
	storekit.WantErr(t, put(changed), kernel.ErrConflict)
	changed = options
	changed.SyncDeletes = true
	storekit.WantErr(t, put(changed), kernel.ErrConflict)
	storekit.Same(t, "rejected replay preserves private options", options, storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, run.Reference)))
	storekit.Same(t, "rejected replay preserves progressed run", next, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	wrong := run.Reference
	wrong.Incarnation += "recreated"
	_, err = progress.Options(ctx, wrong)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, wrong, options) }), kernel.ErrNotFound)
	ordinary := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "stored-run", executionBinding(""), storekit.At(2)))
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, ordinary.Reference, options) }), kernel.ErrConflict)

	t.Run("first options cannot follow execution progress", func(t *testing.T) {
		ctx := t.Context()
		run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "running-without-options", binding, storekit.At(1)))
		next := run
		next.Version++
		next.Phase, next.Updated = model.ExecutionRunning, storekit.At(2)
		storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, run.Version, next) }))
		err := db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, options) })
		storekit.WantErr(t, err, kernel.ErrConflict)
		got, err := progress.Options(ctx, run.Reference)
		storekit.WantErr(t, err, kernel.ErrNotFound)
		storekit.Same(t, "rejected late options return no private values", model.ExecutionOptions{}, got)
		storekit.Same(t, "rejected late options preserve progressed run", next, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	})

	for _, tc := range []struct {
		name   string
		change func(*model.ExecutionBinding, *model.ExecutionOptions)
	}{
		{name: "exact binding"},
		{name: "another repository", change: func(_ *model.ExecutionBinding, o *model.ExecutionOptions) { o.RepositoryKey = "another-repository" }},
		{name: "another target", change: func(_ *model.ExecutionBinding, o *model.ExecutionOptions) { o.TargetKey = "another-target" }},
		{name: "another target revision", change: func(_ *model.ExecutionBinding, o *model.ExecutionOptions) { o.TargetRevision = "another-revision" }},
		{name: "pull direction", change: func(b *model.ExecutionBinding, _ *model.ExecutionOptions) { b.Direction = model.ReplicationPull }},
	} {
		t.Run("independent options "+tc.name, func(t *testing.T) {
			ctx := t.Context()
			binding := executionBinding("")
			binding.OneShot = true
			independent := options
			independent.Independent = true
			independent.RepositoryKey, independent.TargetKey, independent.TargetRevision = binding.Repository.Key, binding.TargetKey, binding.TargetRevision
			if tc.change != nil {
				tc.change(&binding, &independent)
			}
			run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "independent-options-"+tc.name, binding, storekit.At(1)))
			err := db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, independent) })
			if tc.change == nil {
				storekit.Must(t, err)
				storekit.Same(t, "exact independent binding is retained", independent,
					storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, run.Reference)))
			} else {
				storekit.WantErr(t, err, kernel.ErrConflict)
				got, err := progress.Options(ctx, run.Reference)
				storekit.WantErr(t, err, kernel.ErrNotFound)
				storekit.Same(t, "rejected binding leaves no options", model.ExecutionOptions{}, got)
			}
			storekit.Same(t, "binding admission preserves execution", run, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
		})
	}
}
