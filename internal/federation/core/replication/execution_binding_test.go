package replication_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (w *executionWorld) executionStep(ctx context.Context, run model.ExecutionRun, progress port.ExecutionProgressStore) error {
	if w.push != nil {
		return w.push.push.ExecutionStep(ctx, run, progress, func(context.Context) error { return nil })
	}
	return w.pull.driver.ExecutionStep(ctx, run, progress, func(context.Context) error { return nil })
}

func TestUnitReplicationExecutionCannotBorrowAnotherDirectionLifetime(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		for _, mismatch := range []string{"missing transfer", "target revision", "repository lifetime", "author", "scope"} {
			t.Run(string(direction)+"/"+mismatch, func(t *testing.T) {
				w := newExecutionWorld(t, direction)
				borrowed := w.run
				var want error = kernel.ErrConflict
				switch mismatch {
				case "missing transfer":
					borrowed.Binding.TransferIncarnation = "another-transfer-lifetime"
					want = kernel.ErrNotFound
				case "target revision":
					borrowed.Binding.TargetRevision = "another-target-revision"
				case "repository lifetime":
					borrowed.Binding.Repository.Incarnation = "recreated-repository"
				case "author":
					borrowed.Binding.Credential.TokenID = "another-author-token"
				case "scope":
					borrowed.Binding.Root = "another-scope"
				}
				if err := w.executionStep(t.Context(), borrowed, w.db.Executions()); !errors.Is(err, want) {
					t.Fatal("execution accepted another private transfer binding", err)
				}
				current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if !reflect.DeepEqual(current, w.run) || direction == model.ReplicationPush && w.push.puts != 0 || direction == model.ReplicationPull && w.pull.fetches != 0 {
					t.Fatal("borrowed transfer binding changed execution or artifact effects")
				}
				must(t, w.worker.Execute(t.Context(), w.item))
				current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if current.Status() != model.ReplicationOK || current.Outcomes.Succeeded != 1 {
					t.Fatal("refused binding prevented the original execution's valid recovery")
				}
			})
		}
	}
}

func TestUnitReplicationUnprojectedTerminalTransferCannotAcknowledgeDispatch(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		t.Run(string(direction), func(t *testing.T) {
			w := newExecutionWorld(t, direction)
			if direction == model.ReplicationPush {
				ref := model.PushReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation}
				for range 20 {
					run, err := w.push.push.Step(t.Context(), ref, func(context.Context) error { return nil })
					must(t, err)
					if run.Phase.Terminal() {
						break
					}
				}
			} else {
				ref := model.PullReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation}
				for range 20 {
					run, err := w.pull.driver.Step(t.Context(), ref, func(context.Context) error { return nil })
					must(t, err)
					if run.Phase.Terminal() {
						break
					}
				}
			}
			if err := w.executionStep(t.Context(), w.run, w.db.Executions()); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("direction-only completion fabricated an atomic execution checkpoint", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if !reflect.DeepEqual(current, w.run) || current.Status() != model.ReplicationIncomplete {
				t.Fatal("unprojected transfer was acknowledged as completed dispatch")
			}
		})
	}
}

func TestUnitReplicationOneShotRecoveryRequiresOriginalPrivateOptions(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		for _, stage := range []string{"options read", "borrowed options"} {
			t.Run(string(direction)+"/"+stage, func(t *testing.T) {
				w := newExecutionWorldWithOptions(t, direction, &replication.ExecutionInput{})
				progress := &faultExecutionProgress{
					ExecutionProgressStore: w.db.Executions(),
					optionsReadError:       stage == "options read",
					borrowedOptions:        stage == "borrowed options",
				}
				w.worker.Store = faultDispatchStore{CoordinatorStore: w.db, progress: progress, queue: w.db.WorkQueue()}
				var want error = kernel.ErrConflict
				if stage == "options read" {
					want = errExecutionStorage
				}
				if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, want) {
					t.Fatal("one-shot dispatch restored unavailable or foreign private options", err)
				}
				// The same port is checked by each actual direction driver even if a
				// caller bypasses the dispatch worker's earlier admission check.
				if err := w.executionStep(t.Context(), w.run, progress); !errors.Is(err, want) {
					t.Fatal("direction bypassed immutable execution options", err)
				}
				current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if !reflect.DeepEqual(current, w.run) || direction == model.ReplicationPush && w.push.puts != 0 || direction == model.ReplicationPull && w.pull.fetches != 0 {
					t.Fatal("unrestored private options created progress or artifact effects")
				}
				progress.optionsReadError, progress.borrowedOptions = false, false
				must(t, w.worker.Execute(t.Context(), w.item))
				current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if current.Status() != model.ReplicationOK || current.Outcomes.Succeeded != 1 {
					t.Fatal("one-shot options recovery lost or repeated actual effects")
				}
			})
		}
	}
}

func TestUnitReplicationExecutionStepRefreshesAdministrationAfterDispatch(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		for _, stage := range []string{"guard", "policy read", "revoked", "repository read", "changed class"} {
			t.Run(string(direction)+"/"+stage, func(t *testing.T) {
				w := newExecutionWorld(t, direction)
				var service *replication.Service
				var core *world
				if direction == model.ReplicationPush {
					service, core = &w.push.push.Configurations, w.push.world
				} else {
					service, core = &w.pull.driver.Configurations, w.pull.world
				}
				original := *service
				var want error = errExecutionStorage
				switch stage {
				case "guard":
					service.Guard = func(context.Context) error { return errExecutionStorage }
				case "policy read":
					service.Authorize = func(context.Context) (replication.Policy, error) { return nil, errExecutionStorage }
				case "revoked":
					service.Authorize = func(context.Context) (replication.Policy, error) { return authority{}, nil }
					want = kernel.ErrForbidden
				case "repository read":
					core.repos.metadataError = errExecutionStorage
				case "changed class":
					repository := core.repos.values[w.run.Binding.Repository.Key]
					if direction == model.ReplicationPush {
						repository.Class = repo.Remote
					} else {
						repository.Class = repo.Local
					}
					core.repos.values[repository.Key] = repository
					want = kernel.ErrConflict
				}
				if err := w.executionStep(t.Context(), w.run, w.db.Executions()); !errors.Is(err, want) {
					t.Fatal("direction effect retained stale administration or repository class", err)
				}
				current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if !reflect.DeepEqual(current, w.run) || direction == model.ReplicationPush && w.push.puts != 0 || direction == model.ReplicationPull && w.pull.fetches != 0 {
					t.Fatal("revoked execution step changed retained progress or artifacts")
				}
				*service = original
			})
		}
	}
}
