package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func executionSelectionCount(t *testing.T, db port.Store) {
	t.Helper()
	run := storekit.Get[model.ExecutionRun](t)(db.Executions().Create(t.Context(), "selected-count", executionBinding(""), storekit.At(1)))
	first := effectFor(run, "selected/a")
	recordEffect(t, db, first)
	retained := effectFor(run, "selected/a")
	retained.OperationKey, retained.Fingerprint = effectHash("retained-a"), effectHash("retained-intent-a")
	retained.Outcome = model.EffectRetained
	recordEffect(t, db, retained)
	recordEffect(t, db, effectFor(run, "selected/b"))
	for _, path := range []string{"selected/a", "selected/b", "unselected"} {
		count, found, err := db.Executions().SelectedPaths(t.Context(), run.Reference, path)
		storekit.Must(t, err)
		if count != 2 || found != (path != "unselected") {
			t.Fatal("selection limit counted reconciliation twice or invented a selected path", count, found)
		}
	}
}
