package replication_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type executionPullPageFault struct {
	artifactsapi.PullSource
	stage  string
	item   artifactsapi.PullItem
	cached artifactsapi.PullCacheCandidate
}

func (s executionPullPageFault) Items(
	ctx context.Context,
	source artifactsapi.PullRepository,
	selection artifactsapi.PullSelection,
	page kernel.Page,
) ([]artifactsapi.PullItem, string, error) {
	if s.stage == "source read" {
		return nil, "", errExecutionStorage
	}
	if s.stage == "source bound" {
		return []artifactsapi.PullItem{s.item, s.item}, "", nil
	}
	if s.stage == "source cursor" {
		return nil, page.After, nil
	}
	if s.stage == "source selection" {
		item := s.item
		item.Path = "private/outside"
		return []artifactsapi.PullItem{item}, "", nil
	}
	return s.PullSource.Items(ctx, source, selection, page)
}

func (s executionPullPageFault) CachedItems(
	ctx context.Context,
	source artifactsapi.PullRepository,
	selection artifactsapi.PullSelection,
	page kernel.Page,
) ([]artifactsapi.PullCacheCandidate, string, error) {
	if s.stage == "cache read" {
		return nil, "", errExecutionStorage
	}
	if s.stage == "cache bound" {
		return []artifactsapi.PullCacheCandidate{s.cached, s.cached}, "", nil
	}
	if s.stage == "cache cursor" {
		return nil, page.After, nil
	}
	return s.PullSource.CachedItems(ctx, source, selection, page)
}

func TestUnitReplicationPullInvalidOwnerPageCannotAdvanceExecution(t *testing.T) {
	for _, stage := range []string{"source read", "source bound", "source cursor", "source selection"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPull)
			ref := model.PullReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation}
			run, err := w.db.PullProgress().Run(t.Context(), ref)
			must(t, err)
			w.pull.add(run, "selected/second")
			if stage == "source cursor" {
				for range 10 {
					must(t, w.executionStep(t.Context(), w.run, w.db.Executions()))
					current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
					must(t, err)
					if current.Outcomes.Succeeded == 1 {
						break
					}
				}
			}
			before, err := w.db.PullProgress().Run(t.Context(), ref)
			must(t, err)
			original := w.pull.driver.Source
			w.pull.driver.Source = executionPullPageFault{PullSource: original, stage: stage, item: w.pull.items[0]}
			var want error = kernel.ErrStore
			if stage == "source selection" {
				want = kernel.ErrInvalid
			}
			if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, want) {
				t.Fatal("invalid owner page acknowledged a bounded pull execution", err)
			}
			current, err := w.db.PullProgress().Run(t.Context(), ref)
			must(t, err)
			wantFetches := 0
			if stage == "source cursor" {
				wantFetches = 1
			}
			if !reflect.DeepEqual(current, before) || w.pull.fetches != wantFetches {
				t.Fatal("invalid owner page changed cursor, intent or cache effects")
			}
			w.pull.driver.Source = original
			must(t, w.worker.Execute(t.Context(), w.item))
			completed, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if completed.Status() != model.ReplicationOK || completed.Outcomes.Succeeded != 2 || w.pull.fetches != 2 {
				t.Fatal("owner-page recovery lost or repeated selected effects")
			}
		})
	}
}

func TestUnitReplicationPullInvalidCachePageCannotAcknowledgeReconciliation(t *testing.T) {
	for _, stage := range []string{"cache read", "cache bound", "cache cursor"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorldWithOptions(t, model.ReplicationPull, &replication.ExecutionInput{SyncDeletes: ptr(true), SyncProperties: ptr(false)})
			ref := model.PullReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation}
			run, err := w.db.PullProgress().Run(t.Context(), ref)
			must(t, err)
			w.pull.items = nil
			for _, path := range []string{"selected/gone", "selected/second"} {
				w.pull.targets = append(
					w.pull.targets,
					artifactsapi.PullCacheCandidate{Repository: w.pull.source, Selection: run.Binding.Selection(), Path: path, Version: strings.Repeat("f", 64)},
				)
			}
			for range 20 {
				must(t, w.executionStep(t.Context(), w.run, w.db.Executions()))
				run, err = w.db.PullProgress().Run(t.Context(), ref)
				must(t, err)
				if run.Phase == model.PullReconcile && (stage != "cache cursor" || w.pull.deletes == 1) {
					break
				}
			}
			if run.Phase != model.PullReconcile || stage == "cache cursor" && run.TargetCursor == "" {
				t.Fatal("control did not reach bounded cache traversal")
			}
			before := run
			original := w.pull.driver.Source
			w.pull.driver.Source = executionPullPageFault{PullSource: original, stage: stage, cached: w.pull.targets[0]}
			if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("invalid cache page acknowledged reconciliation", err)
			}
			current, err := w.db.PullProgress().Run(t.Context(), ref)
			must(t, err)
			wantDeletes := 0
			if stage == "cache cursor" {
				wantDeletes = 1
			}
			if !reflect.DeepEqual(current, before) || w.pull.deletes != wantDeletes {
				t.Fatal("invalid cache page advanced traversal or removed an artifact")
			}
			w.pull.driver.Source = original
			must(t, w.worker.Execute(t.Context(), w.item))
			completed, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if completed.Status() != model.ReplicationOK || completed.Outcomes != (model.ExecutionOutcomes{Succeeded: 2}) || w.pull.deletes != 2 {
				t.Fatal("cache-page recovery lost exact removal/companion evidence")
			}
		})
	}
}
