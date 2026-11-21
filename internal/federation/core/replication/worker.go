package replication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Worker retains the normal queue's retry/shutdown lifecycle while excluding
// competing target transfers. Direction drivers publish exact effects and
// completion together with their private progress, before the supplied fence.
type Worker struct {
	Store          CoordinatorStore
	Configurations Service
	Ownership      Ownership
	Drivers        map[model.ReplicationDirection]TransferStep
	Restore        func(context.Context, kernel.CredentialReference) (context.Context, error)
	Now            func() time.Time
}

type executionInterrupted struct{ cause error }

func (e *executionInterrupted) Error() string { return "replication execution interrupted" }
func (e *executionInterrupted) Unwrap() error { return e.cause }

func (w Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func executionWorkReference(item work.WorkItem) (model.ExecutionReference, error) {
	var ref model.ExecutionReference
	decoder := json.NewDecoder(bytes.NewReader(item.Payload))
	decoder.DisallowUnknownFields()
	if item.Kind != ExecutionWorkKind || len(item.Payload) > 2048 || decoder.Decode(&ref) != nil ||
		ref.Validate() != nil || item.Key != ownershipHash(ref) {
		return model.ExecutionReference{}, fmt.Errorf("invalid replication work reference: %w", work.ErrPermanent)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return model.ExecutionReference{}, fmt.Errorf("trailing replication work payload: %w", work.ErrPermanent)
	}
	return ref, nil
}

// Execute performs bounded steps until actual terminal transfer proof commits.
// Cancellation preserves unfinished progress; exhausted retries settle private
// failure under current queue ownership without performing any artifact effect.
func (w Worker) Execute(ctx context.Context, item work.WorkItem) error {
	if w.Store == nil || w.Configurations.Guard == nil || w.Restore == nil || w.Store.InTx(ctx) {
		return fmt.Errorf("replication worker contracts unavailable: %w", kernel.ErrStore)
	}
	ref, err := executionWorkReference(item)
	if err != nil {
		return err
	}
	run, err := w.Store.Executions().Run(ctx, ref)
	if err != nil {
		return &executionInterrupted{cause: err}
	}
	if run.Phase == model.ExecutionComplete || run.Phase == model.ExecutionAborted {
		return nil
	}
	ownership := w.Ownership
	if ownership.Store == nil {
		ownership.Store = w.Store
	}
	if ownership.Guard == nil {
		ownership.Guard = w.Configurations.Guard
	}
	if ownership.Now == nil {
		ownership.Now = w.Now
	}
	err = ownership.Run(ctx, item, run.Binding.TargetKey, func(ctx context.Context, owner Owner) error {
		fence := func(ctx context.Context) error {
			if err := tx.CheckPublication(ctx); err != nil {
				return fmt.Errorf("settle replication publication: %w", err)
			}
			if err := ownership.Fence(ctx, owner); err != nil {
				return err
			}
			if err := tx.BeforeCommit(ctx, func(ctx context.Context) error { return ownership.Fence(ctx, owner) }); err != nil {
				return fmt.Errorf("register final replication ownership: %w", err)
			}
			return nil
		}
		if err := w.run(ctx, ref, fence); err != nil {
			if !item.FinalAttempt || ctx.Err() != nil {
				return err
			}
			if settlement := w.abort(ctx, ref, executionFailure(err), fence); settlement != nil {
				return errors.Join(work.ErrUnsettled, err, settlement)
			}
		}
		return nil
	})
	if err != nil {
		// Queue LastError receives a closed message rather than transport/DB
		// diagnostics; errors.Is still retains cancellation and retry identity.
		if errors.Is(err, kernel.ErrConflict) {
			// A competitor or lost lease cannot settle this execution. Parking
			// its queue row would strand truthful incomplete progress forever.
			err = errors.Join(work.ErrUnsettled, err)
		}
		return &executionInterrupted{cause: err}
	}
	return nil
}

func (w Worker) run(ctx context.Context, ref model.ExecutionReference, fence PushFence) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("replication dispatch cancelled: %w", err)
		}
		run, err := w.Store.Executions().Run(ctx, ref)
		if err != nil {
			return fmt.Errorf("read replication dispatch: %w", err)
		}
		if run.Phase == model.ExecutionComplete || run.Phase == model.ExecutionAborted {
			return nil
		}
		step := w.Drivers[run.Binding.Direction]
		if step == nil {
			return fmt.Errorf("replication direction driver unavailable: %w", kernel.ErrStore)
		}
		authorized, err := w.Restore(ctx, run.Binding.Credential)
		if err != nil {
			return fmt.Errorf("restore original replication author: %w", err)
		}
		if authorized == nil || w.Store.InTx(authorized) {
			return fmt.Errorf("replication author restoration leaked a transaction: %w", kernel.ErrStore)
		}
		principal, err := kernel.RequireAuthenticated(authorized)
		if err != nil || principal.CredentialReference() != run.Binding.Credential {
			return fmt.Errorf("replication restored a different credential: %w", kernel.ErrBadCredentials)
		}
		if err := w.enter(authorized, run, fence); err != nil {
			return err
		}
		if err := step(authorized, run, fence); err != nil {
			return fmt.Errorf("execute bounded replication transfer: %w", err)
		}
	}
}

func (w Worker) enter(ctx context.Context, run model.ExecutionRun, fence PushFence) error {
	return w.Configurations.run(ctx, func(ctx context.Context, policy Policy) error {
		repository, err := w.Configurations.repository(ctx, run.Binding.Repository.Key, policy)
		if err != nil {
			return err
		}
		if direction, ok := model.DirectionFor(repository.Class); !ok || direction != run.Binding.Direction {
			return kernel.ErrConflict
		}
		return w.Configurations.WithTarget(ctx, run.Binding.TargetKey, run.Binding.TargetRevision, func(ctx context.Context, _ model.Replication) error {
			current, err := w.Store.Executions().Run(ctx, run.Reference)
			if err != nil {
				return fmt.Errorf("lock replication dispatch: %w", err)
			}
			if current.Phase == model.ExecutionPending {
				next := current
				next.Version++
				next.Phase = model.ExecutionRunning
				if now := w.now(); now.After(next.Updated) {
					next.Updated = now
				}
				if err := w.Store.Executions().Advance(ctx, current.Version, next); err != nil {
					return fmt.Errorf("start replication dispatch: %w", err)
				}
			}
			return fence(ctx) //nolint:wrapcheck // lease checks must be last
		})
	})
}

func executionFailure(err error) model.ExecutionFailure {
	switch {
	case errors.Is(err, kernel.ErrForbidden), errors.Is(err, kernel.ErrBadCredentials), errors.Is(err, kernel.ErrUnauthenticated),
		errors.Is(err, kernel.ErrAPIRestricted), errors.Is(err, kernel.ErrPasswordExpired), errors.Is(err, artifactsapi.ErrPullDenied):
		return model.ExecutionAuthorityDenied
	case errors.Is(err, kernel.ErrConflict), errors.Is(err, kernel.ErrNotFound), errors.Is(err, artifactsapi.ErrPullChanged):
		return model.ExecutionConfigurationChanged
	case errors.Is(err, artifactsapi.ErrPullOffline):
		return model.ExecutionSourceOffline
	case errors.Is(err, artifactsapi.ErrPullNotStored):
		return model.ExecutionNotStored
	case errors.Is(err, artifactsapi.ErrPullIntegrity):
		return model.ExecutionIntegrityFailed
	case errors.Is(err, artifactsapi.ErrPullUnsupported), errors.Is(err, ErrPushStatistics):
		return model.ExecutionUnsupported
	default:
		return model.ExecutionRetryExhausted
	}
}

func (w Worker) abort(ctx context.Context, ref model.ExecutionReference, failure model.ExecutionFailure, fence PushFence) error {
	err := w.Store.Tx(ctx, func(ctx context.Context) error {
		if err := w.Configurations.Guard(ctx); err != nil {
			return fmt.Errorf("guard failed replication settlement: %w", err)
		}
		run, err := w.Store.Executions().Run(ctx, ref)
		if err != nil {
			return fmt.Errorf("read failed replication execution: %w", err)
		}
		if run.Phase != model.ExecutionComplete && run.Phase != model.ExecutionAborted {
			next := run
			next.Version++
			next.Phase = model.ExecutionAborted
			next.Failure = failure
			if now := w.now(); now.After(next.Updated) {
				next.Updated = now
			}
			next.Completed = next.Updated
			if err := w.Store.Executions().Advance(ctx, run.Version, next); err != nil {
				return fmt.Errorf("retain failed replication execution: %w", err)
			}
		}
		return fence(ctx) //nolint:wrapcheck // private failure settlement still needs current ownership
	})
	if err != nil {
		return fmt.Errorf("settle exhausted replication: %w", err)
	}
	return nil
}
