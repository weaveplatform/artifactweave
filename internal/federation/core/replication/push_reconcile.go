package replication

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (p Push) reconcilePage(ctx context.Context, run model.PushRun, fence PushFence) error {
	if run.TargetDone {
		return p.advance(ctx, run, nil, fence, func(next *model.PushRun) {
			next.Phase, next.Completed = model.PushComplete, next.Updated
		})
	}
	connection, err := p.connection(ctx, run, nil, fence)
	if err != nil {
		return err
	}
	rows, cursor, err := connection.Files(ctx, run.Binding.Root, kernel.Page{After: run.TargetCursor, Limit: 1})
	if err != nil {
		return fmt.Errorf("enumerate push destination: %w", err)
	}
	if len(rows) > 1 {
		return fmt.Errorf("destination exceeded push page bound: %w", kernel.ErrStore)
	}
	if cursor != "" && cursor == run.TargetCursor {
		return fmt.Errorf("destination push cursor did not advance: %w", kernel.ErrStore)
	}
	scope, err := newPushScope(run.Binding.Root, run.Binding.Include, run.Binding.Exclude)
	if err != nil {
		return err
	}
	advance := func(ctx context.Context, current model.PushRun, create bool) error {
		if create {
			intent := model.PushItem{
				Run: run.Reference, Action: model.PushDelete, Path: rows[0].Path,
				Target: &rows[0].Snapshot, Version: 1, Phase: model.PushIntent, Updated: p.now(),
			}
			if _, err := p.Store.PushProgress().CreateItem(ctx, intent); err != nil {
				return fmt.Errorf("record push deletion intent: %w", err)
			}
		}
		next := current
		next.Version++
		next.Updated, next.TargetCursor, next.TargetDone = p.now(), cursor, cursor == ""
		if err := p.Store.PushProgress().Advance(ctx, current.Version, next); err != nil {
			return fmt.Errorf("record destination enumeration: %w", err)
		}
		return nil
	}
	if len(rows) == 1 && rows[0].Snapshot.Folder {
		return fmt.Errorf("push destination files included a folder: %w", kernel.ErrStore)
	}
	selected := len(rows) == 1 && scope.matches(rows[0].Path)
	if selected {
		absent := false
		gate := func(ctx context.Context, local func(context.Context) error) error {
			return p.Source.WithAbsent(ctx, run.Binding.Source, rows[0].Path, func(ctx context.Context) error {
				absent = true
				return local(ctx)
			})
		}
		err = p.withRun(ctx, run, gate, fence, func(ctx context.Context, current model.PushRun, _ port.PushConnection) error {
			return advance(ctx, current, true)
		})
		if absent || !errors.Is(err, kernel.ErrConflict) {
			return err
		}
		// A currently readable source file protects its destination. Other
		// current binding/version conflicts fail again below, rather than
		// turning a missing/denied repository into an authorized absence.
	}
	return p.withRun(ctx, run, nil, fence, func(ctx context.Context, current model.PushRun, _ port.PushConnection) error {
		return advance(ctx, current, false)
	})
}

func samePushTarget(a, b model.PushTargetSnapshot) bool {
	return a.Folder == b.Folder && a.Size == b.Size && a.Actual == b.Actual && a.Modified.Equal(b.Modified) && a.Updated.Equal(b.Updated)
}

func (p Push) delete(ctx context.Context, run model.PushRun, item model.PushItem, fence PushFence) error {
	gate := p.absenceGate(run, item.Path)
	connection, err := p.connection(ctx, run, gate, fence)
	if err != nil {
		if errors.Is(err, kernel.ErrConflict) {
			return p.outcome(ctx, run, item, fence, model.PushSkipped, "source_present")
		}
		return err
	}
	observed, err := connection.Inspect(ctx, item.Path)
	if errors.Is(err, kernel.ErrNotFound) {
		return p.recordDelete(ctx, run, item, gate, fence)
	}
	if err != nil {
		return p.targetError(ctx, run, item, fence, err)
	}
	if observed.Path != item.Path || !samePushTarget(observed.Snapshot, *item.Target) || observed.Snapshot.Folder {
		return p.outcome(ctx, run, item, fence, model.PushSkipped, "target_changed")
	}
	// A preceding remote read may have blocked: refresh every local condition
	// once more before asking the target to recheck and delete this exact file.
	connection, err = p.connection(ctx, run, gate, fence)
	if err != nil {
		return err
	}
	if err := connection.Delete(ctx, item.Path, *item.Target, p.mutationFence(run, gate, fence)); err != nil {
		var refused *pushMutationRefusal
		if errors.As(err, &refused) {
			return fmt.Errorf("current push deletion authority: %w", err)
		}
		if errors.Is(err, kernel.ErrConflict) {
			return p.outcome(ctx, run, item, fence, model.PushSkipped, "target_changed")
		}
		return p.targetError(ctx, run, item, fence, err)
	}
	connection, err = p.connection(ctx, run, gate, fence)
	if err != nil {
		return err
	}
	if _, err := connection.Inspect(ctx, item.Path); !errors.Is(err, kernel.ErrNotFound) {
		if err != nil {
			return p.targetError(ctx, run, item, fence, err)
		}
		return p.outcome(ctx, run, item, fence, model.PushSkipped, "target_changed")
	}
	return p.recordDelete(ctx, run, item, gate, fence)
}

func (p Push) recordDelete(ctx context.Context, run model.PushRun, item model.PushItem, gate pushGate, fence PushFence) error {
	return p.recordItem(ctx, run, item, gate, fence, func(next *model.PushItem) {
		next.Phase, next.Outcome, next.TargetAbsent = model.PushItemComplete, model.PushSucceeded, true
	})
}
