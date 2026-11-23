package replication

import (
	"context"
	"errors"
	"fmt"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	platformtx "github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// PullRequest names one logical selected transfer under an exact target revision.
type PullRequest struct{ ID, TargetKey, TargetRevision, Root string }

// Pull drives bounded resumable owning-cache work through the artifacts API.
// It stores original caller references and refreshes them before every step.
// The dispatcher owns target exclusion and supplies the final queue lease fence.
type Pull struct {
	Store          port.PullStore
	Configurations Service
	Source         artifactsapi.PullSource
	Restore        func(context.Context, kernel.CredentialReference) (context.Context, error)
	Now            func() time.Time
}

func (p Pull) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (p Pull) available() error {
	if p.Store == nil || p.Source == nil || p.Restore == nil {
		return fmt.Errorf("single-pull contracts unavailable: %w", kernel.ErrStore)
	}
	return nil
}

// Start joins immutable source admission, logical progress and optional queue
// work in one metadata transaction. Source.Repository performs no network I/O.
func (p Pull) Start(ctx context.Context, request PullRequest, admit func(context.Context, model.PullRun) error) (model.PullRun, error) {
	if err := p.available(); err != nil {
		return model.PullRun{}, err
	}
	caller, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return model.PullRun{}, fmt.Errorf("pull admission author: %w", err)
	}
	var run model.PullRun
	err = p.Configurations.WithTarget(ctx, request.TargetKey, request.TargetRevision, func(ctx context.Context, target model.Replication) error {
		if target.SyncStatistics {
			return ErrPushStatistics
		}
		scope, err := newPushScope(request.Root, target.IncludePathPrefix, target.ExcludePathPrefix)
		if err != nil {
			return err
		}
		binding := model.PullBinding{
			TargetKey: target.Key, TargetRevision: target.Revision, Credential: caller.CredentialReference(),
			Root: scope.root, Include: target.IncludePathPrefix, Exclude: target.ExcludePathPrefix,
			SyncDeletes: target.SyncDeletes, SyncProperties: target.SyncProperties,
		}
		binding.Source, err = p.Source.Repository(ctx, target.RepoKey, binding.Selection())
		if err != nil {
			return fmt.Errorf("pull source admission: %w", err)
		}
		run, err = p.Store.PullProgress().Create(ctx, request.ID, binding, p.now())
		if err != nil {
			return fmt.Errorf("create logical pull: %w", err)
		}
		if admit != nil {
			if err := admit(ctx, run); err != nil {
				return fmt.Errorf("publish pull admission: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return model.PullRun{}, fmt.Errorf("start single pull: %w", err)
	}
	return run, nil
}

func (p Pull) withRun(ctx context.Context, expected model.PullRun, fence PushFence,
	effect func(context.Context, model.PullRun) error,
) error {
	return p.Configurations.WithTarget(ctx, expected.Binding.TargetKey, expected.Binding.TargetRevision, func(ctx context.Context, target model.Replication) error {
		currentSource, err := p.Source.Repository(ctx, target.RepoKey, expected.Binding.Selection())
		if err != nil {
			return fmt.Errorf("current pull source: %w", err)
		}
		if currentSource != expected.Binding.Source {
			return fmt.Errorf("pull source/options lifetime changed: %w", kernel.ErrConflict)
		}
		current, err := p.Store.PullProgress().Run(ctx, expected.Reference)
		if err != nil {
			return fmt.Errorf("lock current pull progress: %w", err)
		}
		if current.Version != expected.Version || current.Phase.Terminal() {
			return kernel.ErrConflict
		}
		if effect != nil {
			if err := effect(ctx, current); err != nil {
				return err
			}
		}
		if fence != nil {
			if err := fence(ctx); err != nil {
				return fmt.Errorf("pull execution ownership: %w", err)
			}
		}
		return nil
	})
}

// Step recovers one pending intent before reading another bounded source/cache
// page. All network work stays outside metadata transactions; owner callbacks
// commit actual cache/property/deletion evidence with the selected intent.
func (p Pull) Step(ctx context.Context, ref model.PullReference, fence PushFence) (model.PullRun, error) {
	if err := p.available(); err != nil {
		return model.PullRun{}, err
	}
	if p.Store.InTx(ctx) || fence == nil {
		return model.PullRun{}, kernel.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return model.PullRun{}, fmt.Errorf("pull step cancelled: %w", err)
	}
	run, err := p.Store.PullProgress().Run(ctx, ref)
	if err != nil {
		return model.PullRun{}, fmt.Errorf("read logical pull: %w", err)
	}
	if run.Phase.Terminal() {
		return run, nil
	}
	ctx, err = p.Restore(ctx, run.Binding.Credential)
	if err != nil {
		return run, fmt.Errorf("restore original pull author: %w", err)
	}
	if ctx == nil || p.Store.InTx(ctx) {
		return run, fmt.Errorf("pull author restoration leaked a transaction: %w", kernel.ErrStore)
	}
	// The step's checks outside a transaction resolve the restored author once
	// (#1895); each transaction that records progress or publishes the cache
	// resolves it again, so revocation still stops the step's effects.
	ctx = platformtx.WithSession(ctx)
	if err := p.withRun(ctx, run, fence, nil); err != nil {
		return run, err
	}
	pending, _, err := p.Store.PullProgress().Items(ctx, ref, true, kernel.Page{Limit: 1})
	if err != nil {
		return run, fmt.Errorf("read pending pull intent: %w", err)
	}
	switch {
	case len(pending) != 0:
		err = p.item(ctx, run, pending[0], fence)
	default:
		err = p.page(ctx, run, fence)
	}
	if err != nil {
		return run, fmt.Errorf("single pull step: %w", err)
	}
	current, err := p.Store.PullProgress().Run(ctx, ref)
	if err != nil {
		return run, fmt.Errorf("read advanced pull: %w", err)
	}
	return current, nil
}

// pullPage bounds one enumeration step. The storage file list has no wire
// paging, so each page costs one complete listing of the selected tree; a
// whole page of intents commits with its cursor in one transaction, and the
// following steps fetch them one at a time (#1895). It is kernel.MaxLimit,
// the largest page an owner accepts.
const pullPage = kernel.MaxLimit

func (p Pull) page(ctx context.Context, run model.PullRun, fence PushFence) error {
	limit := pullPage
	for {
		refused, err := p.pageOf(ctx, run, fence, limit)
		if refused <= 0 {
			return err
		}
		// An execution's selection ceiling refused an intent inside the page.
		// Commit only the peer entries before it, so the intents it accepted
		// are fetched first and the refusal recurs at the same point it did
		// when every step selected one intent.
		limit = refused
	}
}

// pageOf records one page and returns the index of an intent the execution
// selection ceiling refused, or -1.
func (p Pull) pageOf(ctx context.Context, run model.PullRun, fence PushFence, limit int) (int, error) {
	next := run
	next.Version++
	next.Updated = p.now()
	var intents []model.PullItem
	switch {
	case run.Phase == model.PullCopy && run.SourceDone:
		if run.Binding.SyncDeletes {
			next.Phase = model.PullReconcile
		} else {
			next.Phase, next.Completed = model.PullComplete, next.Updated
		}
	case run.Phase == model.PullReconcile && run.TargetDone:
		next.Phase, next.Completed = model.PullComplete, next.Updated
	default:
		selected, cursor, err := p.enumerate(ctx, run, limit)
		if err != nil {
			return -1, err
		}
		intents = selected
		if run.Phase == model.PullCopy {
			next.SourceCursor, next.SourceDone = cursor, cursor == ""
		} else {
			next.TargetCursor, next.TargetDone = cursor, cursor == ""
		}
	}
	refused := -1
	err := p.withRun(ctx, run, fence, func(ctx context.Context, current model.PullRun) error {
		for index, intent := range intents {
			if _, err := p.Store.PullProgress().CreateItem(ctx, intent); err != nil {
				if errors.Is(err, ErrSelectionLimit) {
					refused = index
				}
				return fmt.Errorf("record immutable pull intent: %w", err)
			}
		}
		if err := p.Store.PullProgress().Advance(ctx, current.Version, next); err != nil {
			return fmt.Errorf("advance pull traversal: %w", err)
		}
		return nil
	})
	return refused, err
}

func (p Pull) enumerate(ctx context.Context, run model.PullRun, limit int) ([]model.PullItem, string, error) {
	page := kernel.Page{Limit: limit}
	var intents []model.PullItem
	var cursor, previous string
	if run.Phase == model.PullCopy {
		previous = run.SourceCursor
		page.After = previous
		rows, next, err := p.Source.Items(ctx, run.Binding.Source, run.Binding.Selection(), page)
		if err != nil {
			return nil, "", fmt.Errorf("enumerate selected pull source: %w", err)
		}
		if len(rows) > limit {
			return nil, "", kernel.ErrStore
		}
		cursor = next
		for index := range rows {
			intents = append(intents, model.PullItem{
				Run: run.Reference, Version: 1, Phase: model.PullIntent, Updated: p.now(),
				Action: model.PullFetch, Path: rows[index].Path, Source: &rows[index],
			})
		}
	} else {
		previous = run.TargetCursor
		page.After = previous
		rows, next, err := p.Source.CachedItems(ctx, run.Binding.Source, run.Binding.Selection(), page)
		if err != nil {
			return nil, "", fmt.Errorf("enumerate selected pull cache: %w", err)
		}
		if len(rows) > limit {
			return nil, "", kernel.ErrStore
		}
		cursor = next
		for index := range rows {
			intents = append(intents, model.PullItem{
				Run: run.Reference, Version: 1, Phase: model.PullIntent, Updated: p.now(),
				Action: model.PullDelete, Path: rows[index].Path, Target: &rows[index],
			})
		}
	}
	if cursor != "" && cursor == previous {
		return nil, "", fmt.Errorf("pull enumeration did not advance: %w", kernel.ErrStore)
	}
	seen := make(map[string]bool, len(intents))
	for _, intent := range intents {
		if seen[intent.Path] {
			// One page names each selected path once; a repeat is an owner
			// fault, not a second intent for the same file.
			return nil, "", fmt.Errorf("pull enumeration repeated %q: %w", intent.Path, kernel.ErrStore)
		}
		seen[intent.Path] = true
		if err := intent.Validate(run); err != nil {
			return nil, "", fmt.Errorf("artifact owner escaped pull selection: %w", err)
		}
	}
	return intents, cursor, nil
}

func (p Pull) withItem(ctx context.Context, run model.PullRun, expected model.PullItem,
	effect func(context.Context, model.PullItem) error,
) error {
	return p.withRun(ctx, run, nil, func(ctx context.Context, _ model.PullRun) error {
		current, err := p.Store.PullProgress().Item(ctx, run.Reference, expected.Action, expected.Path)
		if err != nil {
			return fmt.Errorf("lock selected pull intent: %w", err)
		}
		if current.Version != expected.Version || current.Phase != model.PullIntent {
			return kernel.ErrConflict
		}
		if effect != nil {
			return effect(ctx, current)
		}
		return nil
	})
}

func (p Pull) item(ctx context.Context, run model.PullRun, intent model.PullItem, fence PushFence) error {
	recorded := false
	publication := artifactsapi.PullPublication{Check: func(ctx context.Context) error { return p.withItem(ctx, run, intent, nil) }, Fence: fence}
	record := func(ctx context.Context, effect func(*model.PullItem)) error {
		return p.withItem(ctx, run, intent, func(ctx context.Context, current model.PullItem) error {
			next := current
			next.Version++
			next.Updated, next.Phase, next.Outcome = p.now(), model.PullItemComplete, model.PullSucceeded
			effect(&next)
			if err := p.Store.PullProgress().AdvanceItem(ctx, current.Version, next); err != nil {
				return fmt.Errorf("record actual pull effect: %w", err)
			}
			recorded = true
			return nil
		})
	}
	var err error
	if intent.Action == model.PullFetch {
		publication.Record = func(ctx context.Context, receipt artifactsapi.PullCached) error {
			return record(ctx, func(next *model.PullItem) { next.Cache = &receipt })
		}
		_, err = p.Source.Fetch(ctx, *intent.Source, publication)
	} else {
		_, err = p.Source.Remove(ctx, *intent.Target, publication, func(ctx context.Context, paths []string) error {
			return record(ctx, func(next *model.PullItem) {
				next.Removed = paths
				if len(paths) == 0 {
					next.Outcome = model.PullRetained
				}
			})
		})
	}
	if err == nil {
		if !recorded {
			return fmt.Errorf("artifact owner omitted atomic pull checkpoint: %w", kernel.ErrStore)
		}
		return nil
	}
	return p.refusal(ctx, run, intent, fence, err)
}

func (p Pull) refusal(ctx context.Context, run model.PullRun, intent model.PullItem, fence PushFence, cause error) error {
	outcome, reason := model.PullSkipped, "source_changed"
	switch {
	case errors.Is(cause, artifactsapi.ErrPullChanged), errors.Is(cause, kernel.ErrConflict):
	case errors.Is(cause, artifactsapi.ErrPullNotStored):
		outcome, reason = model.PullItemFailed, "not_stored"
	case errors.Is(cause, artifactsapi.ErrPullIntegrity):
		outcome, reason = model.PullItemFailed, "integrity"
	case errors.Is(cause, artifactsapi.ErrPullUnsupported):
		outcome, reason = model.PullItemFailed, "unsupported"
	default:
		return cause
	}
	return p.withItem(ctx, run, intent, func(ctx context.Context, current model.PullItem) error {
		next := current
		next.Version++
		next.Updated, next.Phase, next.Outcome, next.Reason = p.now(), model.PullItemComplete, outcome, reason
		if err := p.Store.PullProgress().AdvanceItem(ctx, current.Version, next); err != nil {
			return fmt.Errorf("record safe pull refusal: %w", err)
		}
		return fence(ctx) //nolint:wrapcheck // the final lease fence owns all failure publication
	})
}
