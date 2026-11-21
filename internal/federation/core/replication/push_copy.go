package replication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (p Push) copyPage(ctx context.Context, run model.PushRun, fence PushFence) error {
	if run.SourceDone {
		return p.advance(ctx, run, nil, fence, func(next *model.PushRun) {
			if run.Binding.SyncDeletes {
				next.Phase = model.PushReconcile
			} else {
				next.Phase, next.Completed = model.PushComplete, next.Updated
			}
		})
	}
	// Enumeration has its own short source-authorized transaction. Recording
	// at most one snapshot keeps every source lock before the progress lock.
	if _, err := p.connection(ctx, run, nil, fence); err != nil {
		return err
	}
	selection := artifactsapi.TransferSelection{
		Root: run.Binding.Root, Include: run.Binding.Include, Exclude: run.Binding.Exclude,
		Properties: run.Binding.SyncProperties,
	}
	rows, cursor, err := p.Source.Items(ctx, run.Binding.Source, selection, kernel.Page{After: run.SourceCursor, Limit: 1})
	if err != nil {
		return fmt.Errorf("enumerate push source: %w", err)
	}
	if len(rows) > 1 {
		return fmt.Errorf("source exceeded push page bound: %w", kernel.ErrStore)
	}
	if cursor != "" && cursor == run.SourceCursor {
		return fmt.Errorf("source push cursor did not advance: %w", kernel.ErrStore)
	}
	var gate pushGate
	if len(rows) == 1 {
		scope, err := newPushScope(run.Binding.Root, run.Binding.Include, run.Binding.Exclude)
		if err != nil {
			return err
		}
		if !scope.matches(rows[0].Path) || rows[0].Repository != run.Binding.Source {
			return fmt.Errorf("push source escaped selection: %w", kernel.ErrStore)
		}
		gate = p.sourceGate(rows[0])
	}
	return p.withRun(ctx, run, gate, fence, func(ctx context.Context, current model.PushRun, _ port.PushConnection) error {
		if len(rows) == 1 {
			intent := model.PushItem{Run: run.Reference, Action: model.PushUpload, Path: rows[0].Path, Source: &rows[0], Version: 1, Phase: model.PushIntent, Updated: p.now()}
			if _, err := p.Store.PushProgress().CreateItem(ctx, intent); err != nil {
				return fmt.Errorf("record push source intent: %w", err)
			}
		}
		next := current
		next.Version++
		next.Updated, next.SourceCursor, next.SourceDone = p.now(), cursor, cursor == ""
		if err := p.Store.PushProgress().Advance(ctx, current.Version, next); err != nil {
			return fmt.Errorf("record source enumeration: %w", err)
		}
		return nil
	})
}

func samePushContent(source artifactsapi.TransferItem, target port.PushTargetItem) bool {
	return target.Path == source.Path && target.Snapshot.Folder == source.Folder &&
		(source.Folder || target.Snapshot.Size == source.Size && target.Snapshot.Actual.SHA256 == source.Actual.SHA256)
}

func samePushProperties(a, b map[string][]string) bool {
	return maps.EqualFunc(a, b, slices.Equal[[]string])
}

func (p Push) item(ctx context.Context, run model.PushRun, item model.PushItem, fence PushFence) error {
	if item.Action == model.PushDelete {
		return p.delete(ctx, run, item, fence)
	}
	connection, err := p.connection(ctx, run, p.sourceGate(*item.Source), fence)
	if err != nil {
		if errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound) {
			return p.outcome(ctx, run, item, fence, model.PushSkipped, "source_changed")
		}
		return err
	}
	observed, err := connection.Inspect(ctx, item.Path)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return p.targetError(ctx, run, item, fence, err)
	}
	if item.Phase == model.PushIntent {
		if errors.Is(err, kernel.ErrNotFound) || !samePushContent(*item.Source, observed) {
			if err := p.upload(ctx, run, item, fence); err != nil {
				return p.targetError(ctx, run, item, fence, err)
			}
			connection, err = p.connection(ctx, run, p.sourceGate(*item.Source), fence)
			if err != nil {
				return p.changedSource(ctx, run, item, fence, err)
			}
			observed, err = connection.Inspect(ctx, item.Path)
			if errors.Is(err, kernel.ErrNotFound) {
				return p.targetError(ctx, run, item, fence, port.PushTargetChecksum)
			}
			if err != nil {
				return p.targetError(ctx, run, item, fence, err)
			}
			if !samePushContent(*item.Source, observed) {
				return p.targetError(ctx, run, item, fence, port.PushTargetChecksum)
			}
		}
		err = p.recordItem(ctx, run, item, p.sourceGate(*item.Source), fence, func(next *model.PushItem) {
			next.Phase, next.VerifiedTarget = model.PushContentVerified, &observed.Snapshot
		})
		return p.changedSource(ctx, run, item, fence, err)
	}
	if err != nil || !samePushContent(*item.Source, observed) {
		return p.outcome(ctx, run, item, fence, model.PushSkipped, "target_changed")
	}
	if item.Phase == model.PushContentVerified && run.Binding.SyncProperties {
		if !samePushProperties(item.Source.Properties, observed.Properties) {
			if err := connection.Properties(ctx, item.Path, item.Source.Properties, p.mutationFence(run, p.sourceGate(*item.Source), fence)); err != nil {
				return p.targetError(ctx, run, item, fence, err)
			}
			connection, err = p.connection(ctx, run, p.sourceGate(*item.Source), fence)
			if err != nil {
				return p.changedSource(ctx, run, item, fence, err)
			}
			observed, err = connection.Inspect(ctx, item.Path)
			if err != nil {
				return p.targetError(ctx, run, item, fence, err)
			}
			if !samePushContent(*item.Source, observed) || !samePushProperties(item.Source.Properties, observed.Properties) {
				return p.targetError(ctx, run, item, fence, port.PushTargetMetadata)
			}
		}
		err = p.recordItem(ctx, run, item, p.sourceGate(*item.Source), fence, func(next *model.PushItem) {
			next.Phase, next.VerifiedTarget = model.PushMetadataVerified, &observed.Snapshot
		})
		return p.changedSource(ctx, run, item, fence, err)
	}
	if run.Binding.SyncProperties && !samePushProperties(item.Source.Properties, observed.Properties) {
		return p.outcome(ctx, run, item, fence, model.PushSkipped, "target_changed")
	}
	err = p.recordItem(ctx, run, item, p.sourceGate(*item.Source), fence, func(next *model.PushItem) {
		next.Phase, next.Outcome, next.VerifiedTarget = model.PushItemComplete, model.PushSucceeded, &observed.Snapshot
	})
	return p.changedSource(ctx, run, item, fence, err)
}

func (p Push) changedSource(ctx context.Context, run model.PushRun, item model.PushItem, fence PushFence, err error) error {
	if errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound) {
		return p.outcome(ctx, run, item, fence, model.PushSkipped, "source_changed")
	}
	return err
}

func (p Push) upload(ctx context.Context, run model.PushRun, item model.PushItem, fence PushFence) (err error) {
	var body io.ReadSeekCloser
	if !item.Source.Folder {
		body, err = p.Source.Open(ctx, *item.Source)
		if err != nil {
			return fmt.Errorf("open selected push content: %w", err)
		}
		defer func() {
			if closeErr := body.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close push source content: %w", closeErr))
			}
		}()
	}
	// Opening an immutable body is not authorization to send it after a slow
	// open. Refresh source/config/transport and work ownership immediately first.
	connection, err := p.connection(ctx, run, p.sourceGate(*item.Source), fence)
	if err != nil {
		return err
	}
	var content io.ReadSeeker
	if body != nil {
		content = body
	}
	if err := connection.Put(ctx, *item.Source, content, p.mutationFence(run, p.sourceGate(*item.Source), fence)); err != nil {
		return fmt.Errorf("upload selected push content: %w", err)
	}
	return nil
}
