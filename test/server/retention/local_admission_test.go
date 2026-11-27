package retention_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// Hidden Local admissions protect future migration, while ordinary Local
// cleanup keeps its public-statistics selection and existing deletion behavior.
func TestAccCleanupLocalAdmissionPreservesSelectionReference(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	before := storekit.Get[artmodel.ItemStats](t)(f.db.Items().Stats(f.ctx, f.ids[0]))
	exclusions := f.db.(interface {
		CleanupExclusions() artifactsapi.CleanupExclusions
	}).CleanupExclusions()
	token := storekit.Get[artifactsapi.CleanupAdmission](t)(exclusions.Admit(f.ctx, "execution", f.ids[0]))
	if token.ID == "" {
		t.Fatal("hosted Local response lacks migration protection")
	}
	storekit.Same(t, "private admission leaves public statistics unchanged", before, storekit.Get[artmodel.ItemStats](t)(f.db.Items().Stats(f.ctx, f.ids[0])))
	run, item := f.selected(t)
	storekit.Must(t, f.executor.Execute(f.ctx, item))
	outcome := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
	if outcome.Status != retapi.RunCompleted || outcome.CleanedItems != 2 {
		t.Fatal("Local admission changed ordinary cleanup", outcome.Status, outcome.CleanedItems)
	}
	storekit.Must(t, exclusions.Complete(f.ctx, token))
}
