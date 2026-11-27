package retention_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// A known native refusal/fault keeps its existing outcome/retry semantics while
// every prepared participant is durably settled before the run can finish.
func TestAccFederatedCleanupErrorSettlementReference(t *testing.T) {
	for _, scenario := range []string{"denied_item", "final_native_fault"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			enableFederatedExecutor(t, f)
			run, item := f.selected(t)
			calls := 0
			switch scenario {
			case "denied_item":
				f.executor.Removal = cleanupDeniedItem{ItemRemoval: f.executor.Removal, id: f.ids[0]}
			case "final_native_fault":
				f.executor.Removal = cleanupPersistentItemFault{ItemRemoval: f.executor.Removal, id: f.ids[0], beforeFailure: func() { calls++ }}
				item.FinalAttempt = true
			}
			if scenario == "denied_item" {
				storekit.WantErr(t, f.executor.Execute(f.ctx, item), work.ErrUnsettled)
				waiting := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
				refused := storekit.Get[model.CleanupEntry](t)(f.db.Cleanup().Entry(f.ctx, run.ID, model.CleanupItemEntry, f.ids[0]))
				active := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
				if !waiting.HadErrors || waiting.CleanupIntentID == "" || refused.State != model.CleanupEntryFailed || refused.Reason != "delete_permission_denied" ||
					active.Status.Terminal() {
					t.Fatal("known denial lost its durable outcome/recovery boundary", waiting, refused.State, active.Status)
				}
				exclusions := f.db.(interface {
					CleanupExclusions() artifactsapi.CleanupExclusions
				}).CleanupExclusions()
				_, err := exclusions.Admit(f.ctx, "execution", f.ids[0])
				storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
			}
			storekit.Must(t, f.executor.Execute(f.ctx, item))
			outcome := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
			progress := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			if progress.CleanupIntentID != "" {
				t.Fatal("terminal native failure retained unsettled exclusion", progress.CleanupIntentID)
			}
			if scenario == "denied_item" {
				if outcome.CleanedItems != 1 || outcome.Status != retapi.RunCompletedWithErrors || !progress.HadErrors {
					t.Fatal("coordinated denial lost normal item/error outcome", outcome.Status, outcome.CleanedItems, progress.HadErrors)
				}
			} else {
				if calls != 1 || outcome.Status != retapi.RunFailed || outcome.CleanedItems != 0 || progress.Reason != "work_retries_exhausted" {
					t.Fatal("final native fault did not settle and exhaust exactly once", calls, outcome.Status, outcome.CleanedItems, progress.Reason)
				}
				f.preserved(t)
			}
		})
	}
}
