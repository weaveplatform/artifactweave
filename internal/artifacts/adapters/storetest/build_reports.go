package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// testBuildReports covers the reports keyed build actions keep for their
// replays (decision 0043): stored as given, locked only in a transaction,
// kept past a delete of the runs they name, and pruned by expiry, oldest
// first and at most limit at a time.
func testBuildReports(t *testing.T, s port.Store) {
	ctx := context.Background()
	bs := s.Builds()
	run := storekit.Get[model.Build](t)(bs.Put(ctx, fxBuild("proj", "app", "1", 0)))
	report := func(action string, expires int) model.BuildReport {
		return model.BuildReport{
			Action: action, ProjectKey: "proj", Name: "app", BuildID: run.ID,
			Report:  []byte(`{"messages":[{"Level":"info","Text":"moved"}]}`),
			Created: storekit.At(0), Expires: storekit.At(expires),
		}
	}
	late, early, tie := report("promote", 9), report("delete", 3), report("delete", 3)
	late.BuildID = 0
	ids := make([]int64, 0, 3)
	for _, r := range []model.BuildReport{late, early, tie} {
		id := storekit.Get[int64](t)(bs.PutReport(ctx, r))
		if id <= 0 {
			t.Fatalf("PutReport assigned %d", id)
		}
		ids = append(ids, id)
	}
	locked := func(id int64) (model.BuildReport, error) {
		var out model.BuildReport
		err := s.Tx(ctx, func(ctx context.Context) error {
			var err error
			out, err = bs.ReportForUpdate(ctx, id)
			return err //nolint:wrapcheck // the transaction returns the store's own error
		})
		return out, err //nolint:wrapcheck // storekit.Get reports the store's own error
	}
	late.ID = ids[0]
	storekit.Same(t, "ReportForUpdate", late, storekit.Get[model.BuildReport](t)(locked(ids[0])))
	// The runs a report names may go; a delete's report outlives them.
	if _, err := bs.DeleteAll(ctx, "proj", "app"); err != nil {
		t.Fatal(err)
	}
	early.ID = ids[1]
	storekit.Same(t, "report of deleted runs", early,
		storekit.Get[model.BuildReport](t)(locked(ids[1])))

	_, err := bs.ReportForUpdate(ctx, ids[0])
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = locked(1 << 40)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	for _, bad := range []model.BuildReport{
		{Name: "app", Created: storekit.At(0), Expires: storekit.At(1)},
		{Action: "delete", Name: "app", Created: storekit.At(1), Expires: storekit.At(1)},
	} {
		_, err := bs.PutReport(ctx, bad)
		storekit.WantErr(t, err, kernel.ErrInvalid)
	}
	for _, limit := range []int{0, kernel.MaxLimit + 1} {
		_, err := bs.PruneReports(ctx, storekit.At(10), limit)
		storekit.WantErr(t, err, kernel.ErrInvalid)
	}

	// Nothing has expired before its expiry, which is inclusive.
	if n := storekit.Get[int64](t)(bs.PruneReports(ctx, storekit.At(2), 10)); n != 0 {
		t.Fatalf("pruned %d before any expiry", n)
	}
	// The limit holds, and the earlier expiry and then the lower ID go first.
	if n := storekit.Get[int64](t)(bs.PruneReports(ctx, storekit.At(9), 1)); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	_, err = locked(ids[1])
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Get[model.BuildReport](t)(locked(ids[2]))
	if n := storekit.Get[int64](t)(bs.PruneReports(ctx, storekit.At(9), 10)); n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	for _, id := range ids {
		_, err = locked(id)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	}
}
