package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func testCleanupReportDownload(t *testing.T, store port.Store) {
	t.Helper()
	ctx, cs := t.Context(), store.Cleanup()
	r := storekit.Get[model.CleanupRun](t)(cs.CreateRun(ctx, model.CleanupRun{PolicyKey: "download", ItemType: model.CleanupPackage, Status: api.RunInProgress}))
	storekit.WantErr(t, cs.MarkReportDownloaded(ctx, r.ID), kernel.ErrInvalid)
	err := store.Tx(ctx, func(ctx context.Context) error { return cs.MarkReportDownloaded(ctx, r.ID) })
	storekit.WantErr(t, err, kernel.ErrInvalid)
	if storekit.Get[model.CleanupRun](t)(cs.Run(ctx, r.ID)).ReportDownloaded {
		t.Fatal("incomplete report marked downloaded")
	}
	r.Status, r.Ended = api.RunCompleted, storekit.At(3)
	storekit.Must(t, cs.UpdateRun(ctx, r))
	err = store.Tx(ctx, func(ctx context.Context) error {
		if err := cs.MarkReportDownloaded(ctx, r.ID); err != nil {
			return fmt.Errorf("mark before rollback: %w", err)
		}
		return errExecutionRollback
	})
	storekit.WantErr(t, err, errExecutionRollback)
	if storekit.Get[model.CleanupRun](t)(cs.Run(ctx, r.ID)).ReportDownloaded {
		t.Fatal("rolled back download marker escaped")
	}
	for range 2 {
		storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return cs.MarkReportDownloaded(ctx, r.ID) }))
	}
	got := storekit.Get[model.CleanupRun](t)(cs.Run(ctx, r.ID))
	r.ReportDownloaded = true
	storekit.Same(t, "download flag changes no counters or execution state", r, got)
	err = store.Tx(ctx, func(ctx context.Context) error { return cs.MarkReportDownloaded(ctx, r.ID+100) })
	storekit.WantErr(t, err, kernel.ErrNotFound)
}
