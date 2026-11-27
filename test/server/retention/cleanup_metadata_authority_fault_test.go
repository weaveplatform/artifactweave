package retention_test

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	retport "github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

type cleanupHistoryPageFault struct{ retport.CleanupStore }

func (s cleanupHistoryPageFault) RunPage(context.Context, retport.RunListing) (retport.RunPage, error) {
	return retport.RunPage{}, errExecutorCoordination
}

type cleanupScheduleReadFault struct{ schedule.Store }

func (s cleanupScheduleReadFault) Get(context.Context, schedule.Reference) (schedule.State, error) {
	return schedule.State{}, errExecutorCoordination
}

type cleanupCredentialAuthority struct {
	cleanup.Policy
	reference kernel.CredentialReference
}

func (s cleanupCredentialAuthority) Credential() kernel.CredentialReference { return s.reference }

// Read failures must not escape as a successful partial policy/history answer.
// Original run credentials do not stand in for current report authority.
func TestAccCleanupMetadataOutagesDiscloseNoPartialHistory(t *testing.T) {
	for _, mode := range []string{"policy_list", "last_run", "run_page", "run_metadata", "report_execution", "missing_schedule", "schedule_read"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			run, _ := f.selected(t)
			service := f.policies
			reads := 0
			faultStore := storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, operation string) error {
				if operation == "Execution" {
					reads++
				}
				if mode == "policy_list" && operation == "Policies" || mode == "run_metadata" && operation == "RunMetadata" ||
					mode == "report_execution" && operation == "Execution" && reads == 2 {
					return errExecutorCoordination
				}
				return nil
			}}
			var rows retport.CleanupStore = faultStore
			if mode == "last_run" || mode == "run_page" {
				rows = cleanupHistoryPageFault{rows}
			}
			service.Store = cleanupExecutionBoundaryStore{f.store, rows}
			if mode == "missing_schedule" {
				service.Schedules = nil
			}
			if mode == "schedule_read" {
				service.Schedules = cleanupScheduleReadFault{service.Schedules}
			}
			expected := errExecutorCoordination
			switch mode {
			case "run_page":
				answer, err := service.Runs(f.caller, cleanup.RunViewQuery{ItemTypes: []model.CleanupItemType{model.CleanupPackage}})
				storekit.WantErr(t, err, expected)
				if len(answer.Runs) != 0 || answer.Total != 0 {
					t.Fatal("run page disclosed partial history")
				}
			case "run_metadata":
				answer, err := service.Run(f.caller, "execution-policy", run.ID)
				storekit.WantErr(t, err, expected)
				if answer.Run.ID != 0 {
					t.Fatal("failed run metadata disclosed outcome")
				}
			case "report_execution":
				answer, err := service.Report(f.caller, "execution-policy", run.ID)
				storekit.WantErr(t, err, expected)
				if answer.Snapshot.Run.ID != 0 {
					t.Fatal("failed report disclosed partial history")
				}
			default:
				if mode == "missing_schedule" {
					expected = kernel.ErrStore
				}
				answer, err := service.PolicyViews(f.caller, cleanup.PolicyViewQuery{ItemTypes: []model.CleanupItemType{model.CleanupPackage}})
				storekit.WantErr(t, err, expected)
				if len(answer) != 0 {
					t.Fatal("policy overview disclosed partial results")
				}
			}
			f.preserved(t)
			current := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
			if current.CleanedItems != 0 || current.Status.Terminal() {
				t.Fatal("read outage mutated cleanup execution", current.Status)
			}
		})
	}
}

func TestAccCleanupProjectMetadataOutagesCannotBorrowAuthority(t *testing.T) {
	for _, mode := range []string{"policy_lifetime", "run_lifetime", "project_label_error", "project_label_empty", "project_reader", "enable_lifetime"} {
		t.Run(mode, func(t *testing.T) {
			w := newSelectionWorld(t, inmem.New())
			ctx := t.Context()
			input := selectionInput(repo.Generic)
			storekit.Get[model.CleanupPolicy](t)(w.service.Create(ctx, input.Key, input))
			service := w.service
			switch mode {
			case "policy_lifetime", "run_lifetime", "project_label_error", "enable_lifetime":
				service.ProjectIncarnation = func(context.Context, string) (string, error) { return "", errExecutorCoordination }
			case "project_label_empty":
				service.ProjectDisplayName = func(context.Context, string) (string, error) { return "", nil }
			case "project_reader":
				service.ProjectExists = nil
			}
			switch mode {
			case "run_lifetime":
				answer, err := service.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha", ItemTypes: []model.CleanupItemType{model.CleanupPackage}})
				storekit.WantErr(t, err, errExecutorCoordination)
				if len(answer.Runs) != 0 {
					t.Fatal("unknown project lifetime disclosed history")
				}
			case "enable_lifetime":
				storekit.WantErr(t, service.Enable(ctx, input.Key, true), errExecutorCoordination)
			case "project_reader":
				input.Key = "missing-project-reader"
				_, err := service.Create(ctx, input.Key, input)
				storekit.WantErr(t, err, kernel.ErrStore)
				_, err = w.db.Cleanup().Policy(ctx, input.Key)
				storekit.WantErr(t, err, kernel.ErrNotFound)
			default:
				if mode == "project_label_error" {
					w.authority = "*"
				}
				answer, err := service.PolicyViews(ctx, cleanup.PolicyViewQuery{ProjectKey: "alpha", ItemTypes: []model.CleanupItemType{model.CleanupPackage}})
				if mode == "project_label_empty" {
					storekit.Must(t, err)
					if len(answer) != 1 || answer[0].DisplayProject != "alpha" {
						t.Fatal("empty current label lost stable project identity")
					}
				} else {
					storekit.WantErr(t, err, errExecutorCoordination)
					if len(answer) != 0 {
						t.Fatal("unknown project authority disclosed policy")
					}
				}
			}
		})
	}
}

func TestAccCleanupQueueAndCredentialFailuresPublishNoAuthority(t *testing.T) {
	for _, mode := range []string{"missing_queue", "invalid_credential", "foreign_credential", "enable_invalid_credential", "stop_missing_queue", "missing_index"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			service := f.policies
			if mode == "missing_queue" {
				service.Work = nil
			}
			if mode == "invalid_credential" || mode == "foreign_credential" || mode == "enable_invalid_credential" {
				original := service.Authorize
				reference := kernel.CredentialReference{}
				if mode == "foreign_credential" {
					reference = kernel.CredentialReference{Name: "another-actor", TokenID: "verified-reference"}
				}
				service.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
					policy, err := original(ctx)
					return cleanupCredentialAuthority{policy, reference}, err
				}
			}
			if mode == "enable_invalid_credential" {
				storekit.Must(t, f.policies.Enable(f.caller, "execution-policy", false))
				before := storekit.Get[model.CleanupPolicy](t)(f.db.Cleanup().Policy(f.ctx, "execution-policy"))
				storekit.WantErr(t, service.Enable(f.caller, "execution-policy", true), kernel.ErrBadCredentials)
				storekit.Same(t, "invalid enablement cannot replace generation", before, storekit.Get[model.CleanupPolicy](t)(f.db.Cleanup().Policy(f.ctx, "execution-policy")))
				return
			}
			if mode == "stop_missing_queue" {
				run, _ := f.selected(t)
				before := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
				service.Work = nil
				storekit.WantErr(t, service.Stop(f.caller, run.ID), kernel.ErrStore)
				storekit.Same(t, "stop must durably enqueue settlement or roll back", before, storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID)))
				f.preserved(t)
				return
			}
			if mode == "missing_index" {
				service.PackageIndex = nil
				_, err := service.Select(f.caller, "execution-policy")
				storekit.WantErr(t, err, kernel.ErrStore)
				f.preserved(t)
				return
			}
			_, err := service.ManualRun(f.caller, "execution-policy", false)
			var expected error = kernel.ErrBadCredentials
			if mode == "missing_queue" {
				expected = kernel.ErrStore
			}
			storekit.WantErr(t, err, expected)
			runs, _, err := f.db.Cleanup().Runs(f.ctx, retport.RunFilter{PolicyKey: "execution-policy"}, kernel.Page{Limit: 10})
			storekit.Must(t, err)
			if len(runs) != 0 {
				t.Fatal("failed admission published execution authority")
			}
			f.preserved(t)
		})
	}
}

func TestAccCleanupCancelledHistoryReadDisclosesNothing(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	run, _ := f.selected(t)
	service := f.policies
	ctx, cancel := context.WithCancel(f.caller)
	defer cancel()
	original := service.Authorize
	service.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		authority, err := original(ctx)
		cancel()
		return authority, err
	}
	answer, err := service.Run(ctx, "execution-policy", run.ID)
	storekit.WantErr(t, err, context.Canceled)
	if answer.Run.ID != 0 {
		t.Fatal("cancelled history lookup disclosed retained outcome")
	}
	f.preserved(t)
}

func TestAccCleanupRecreatedProjectCannotInheritPolicyOverview(t *testing.T) {
	w := newSelectionWorld(t, inmem.New())
	ctx := t.Context()
	input := selectionInput(repo.Generic)
	before := storekit.Get[model.CleanupPolicy](t)(w.service.Create(ctx, input.Key, input))
	storekit.Must(t, w.db.Projects().Delete(ctx, "alpha"))
	storekit.Must(t, w.db.Projects().Create(ctx, accesstest.Project{Key: "alpha"}))
	current := storekit.Get[string](t)(w.service.ProjectIncarnation(ctx, "alpha"))
	if current == before.ProjectIncarnation {
		t.Fatal("project fixture did not replace lifetime")
	}
	answer, err := w.service.PolicyViews(ctx, cleanup.PolicyViewQuery{ProjectKey: "alpha", ItemTypes: []model.CleanupItemType{model.CleanupPackage}})
	storekit.Must(t, err)
	if len(answer) != 0 {
		t.Fatal("replacement authority inherited predecessor policy")
	}
}

func TestAccCleanupPackagePolicyReadDoesNotExposeOtherKind(t *testing.T) {
	w := newSelectionWorld(t, inmem.New())
	ctx := t.Context()
	input := selectionInput(repo.Generic)
	existing := storekit.Get[model.CleanupPolicy](t)(w.service.Create(ctx, input.Key, input))
	existing.Key = "future-kind-policy"
	existing.ItemType = model.CleanupBuild
	storekit.Must(t, w.db.Cleanup().CreatePolicy(ctx, existing))
	answer, err := w.service.Get(ctx, existing.Key, false)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	if answer.Key != "" {
		t.Fatal("package endpoint disclosed another policy family")
	}
}
