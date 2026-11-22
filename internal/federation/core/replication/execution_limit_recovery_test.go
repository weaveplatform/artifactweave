package replication_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type executionSelectionFault struct{ port.ExecutionProgressStore }

func (p executionSelectionFault) SelectedPaths(context.Context, model.ExecutionReference, string) (int64, bool, error) {
	return 0, false, errExecutionStorage
}

func TestUnitReplicationSelectionCannotOutgrowAdmittedOrCurrentCeiling(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		for _, scenario := range []struct {
			name                     string
			admitted, current, after int
		}{
			{"lowered while running", 3, 1, 2},
			{"raised while running", 2, 5, 1},
			{"unlimited after admission", 2, 0, 1},
		} {
			t.Run(string(direction)+"/"+scenario.name, func(t *testing.T) {
				w := newExecutionWorldWithLimit(t, direction, nil, scenario.admitted)
				var configuration *replication.Service
				if direction == model.ReplicationPush {
					configuration = &w.push.push.Configurations
					w.push.addSource("selected/second", "second bytes")
					w.push.addSource("selected/third", "third bytes")
				} else {
					configuration = &w.pull.driver.Configurations
					run, err := w.db.PullProgress().Run(t.Context(), model.PullReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation})
					must(t, err)
					w.pull.add(run, "selected/second")
					w.pull.add(run, "selected/third")
				}
				original := w.worker.Drivers[direction]
				changed := false
				w.worker.Drivers[direction] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
					if err := original(ctx, run, fence); err != nil {
						return err
					}
					current, err := w.db.Executions().Run(ctx, run.Reference)
					if err == nil && !changed && current.Outcomes.Succeeded == int64(scenario.after) {
						changed = true
						configuration.UserQueryLimit = scenario.current
					}
					return err
				}
				must(t, w.worker.Execute(t.Context(), w.item))
				current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if !changed || !reflect.DeepEqual(current.Binding, w.run.Binding) || current.Binding.SelectionLimit != int64(scenario.admitted) ||
					current.Phase != model.ExecutionAborted || current.Failure != model.ExecutionSelectionLimited || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 2}) || current.Status() != model.ReplicationPartialFailure {
					t.Fatal("current/admitted ceiling was relaxed or truncation fabricated success", current.Phase, current.Failure, current.Outcomes)
				}
				count, present, err := w.db.Executions().SelectedPaths(t.Context(), current.Reference, "selected/third")
				must(t, err)
				if count != 2 || present {
					t.Fatal("refused third selection retained an intent")
				}
				if direction == model.ReplicationPush && (w.push.puts != 2 || w.push.deletes != 0) ||
					direction == model.ReplicationPull && (w.pull.fetches != 2 || w.pull.deletes != 0) {
					t.Fatal("truncated transfer reached an unselected effect or reconciliation")
				}
			})
		}
	}
}

func TestUnitReplicationSelectionReadFailureRetainsRetryableUnselectedProgress(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		t.Run(string(direction), func(t *testing.T) {
			w := newExecutionWorldWithLimit(t, direction, nil, 2)
			w.projectWith(executionSelectionFault{w.db.Executions()})
			if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("unavailable selected-path count authorized an intent", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Phase != model.ExecutionRunning || current.Outcomes != (model.ExecutionOutcomes{}) || !current.Completed.IsZero() ||
				direction == model.ReplicationPush && w.push.puts != 0 || direction == model.ReplicationPull && w.pull.fetches != 0 {
				t.Fatal("unavailable selection proof created false progress or artifact effects")
			}
			w.projectWith(w.db.Executions())
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
				t.Fatal("selection-count recovery lost or repeated an actual effect")
			}
		})
	}
}
