package replication_test

import (
	"context"
	"errors"
	"fmt"
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

// One enumeration step records a whole owner page (#1895). A selection
// ceiling inside that page still refuses the same intent that one-at-a-time
// enumeration refused, after the intents before it are fetched.
func TestUnitReplicationPullPageKeepsTheSelectionCeilingInPlace(t *testing.T) {
	for _, limit := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			w := newExecutionWorldWithLimit(t, model.ReplicationPull, nil, limit)
			w.pull.pageSize = 100
			run, err := w.db.PullProgress().Run(t.Context(), model.PullReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation})
			must(t, err)
			w.pull.add(run, "selected/second")
			w.pull.add(run, "selected/third")
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			// One listing records every intent when no ceiling meets it.
			want, wantPages := limit, 1
			if limit == 0 || limit == 3 {
				want = 3
			}
			if limit == 1 || limit == 2 {
				// The page that met the ceiling is read again up to it, and the
				// next enumeration meets the ceiling at its first intent.
				wantPages = 3
			}
			if current.Outcomes != (model.ExecutionOutcomes{Succeeded: int64(want)}) || w.pull.fetches != want || w.pull.pages != wantPages {
				t.Fatal("page outcomes", current.Outcomes, w.pull.fetches, w.pull.pages)
			}
			if limit == 1 || limit == 2 {
				if current.Failure != model.ExecutionSelectionLimited {
					t.Fatal("ceiling inside a page was not reported", current.Failure)
				}
				count, present, err := w.db.Executions().SelectedPaths(t.Context(), current.Reference, "selected/third")
				must(t, err)
				if count != int64(limit) || present {
					t.Fatal("refused intent was selected", count, present)
				}
			} else if current.Status() != model.ReplicationOK {
				t.Fatal("unlimited page did not complete", current.Failure)
			}
		})
	}
}
