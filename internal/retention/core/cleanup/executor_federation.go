package cleanup

import (
	"context"
	"errors"
	"fmt"

	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// federatedEffectFailure distinguishes an ordinary rolled-back native fault
// from uncertainty while settling an irreversible cross-member reservation.
// Queue exhaustion may record failure with the reservation still referenced;
// Step always settles that reference before terminal failure publication.
type federatedEffectFailure struct{ cause error }

func (e *federatedEffectFailure) Error() string { return "coordinated cleanup native effect failed" }
func (e *federatedEffectFailure) Unwrap() error { return e.cause }

// Member observations are refreshed before entering the destructive metadata
// transaction. Saved pins explain the candidate; only this fresh permit can
// accompany normal removal. No metadata transaction spans a peer request.
func (x Executor) removeFederated(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, s *Selection, fence func(context.Context) error) (bool, error) {
	bound, err := x.Policies.Restore(ctx, e.Credential)
	if err != nil {
		return false, fmt.Errorf("restore federated cleanup caller: %w", err)
	}
	if bound == nil || x.Store.InTx(bound) {
		return true, fmt.Errorf("federated cleanup retained a transaction: %w", kernel.ErrStore)
	}
	err = s.run(bound, func(ctx context.Context) error {
		_, err := x.currentRun(ctx, e)
		if err != nil {
			return fmt.Errorf("validate current coordinated cleanup run: %w", err)
		}
		return nil
	})
	if err != nil {
		return true, fmt.Errorf("federated cleanup execution: %w", err)
	}
	// A native companion removal can already have settled every pending item.
	var pending bool
	var fileID int64
	err = s.run(bound, func(ctx context.Context) error {
		entry, err := x.Store.Cleanup().PendingEntry(ctx, e.RunID, p.Version.ID)
		fileID = entry.EntityID
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		pending = err == nil
		if err != nil {
			return fmt.Errorf("read federated cleanup intent: %w", err)
		}
		return nil
	})
	if err != nil {
		return true, fmt.Errorf("federated cleanup execution: %w", err)
	}
	if !pending {
		return true, x.withCaller(bound, e.Credential, func(ctx context.Context, authority Policy) error {
			return x.authorizedStep(ctx, e, p, s, fence, authority)
		})
	}
	if p.Federation == nil || x.Policies.Federation == nil {
		return true, fmt.Errorf("federated cleanup candidate has no bound evidence: %w", kernel.ErrConflict)
	}
	if p.Federation.Scope.Receiver {
		return true, x.refuseFederation(bound, e, p, ReasonFederationReceiver, fence)
	}

	evidence, err := x.Policies.Federation.Collect(bound, p.Federation.Scope, []federationapi.CleanupVersion{{Package: p.Version.Package.Name, Version: p.Version.Version}})
	if bound.Err() != nil {
		return true, fmt.Errorf("federated cleanup cancelled: %w", bound.Err())
	}
	if errors.Is(err, federationapi.ErrCleanupUnavailable) {
		return true, x.refuseFederation(bound, e, p, ReasonFederationUnknown, fence)
	}
	if err != nil {
		return true, fmt.Errorf("execute coordinated cleanup: %w", err)
	}
	if reason := s.federationDownloadReason(evidence); reason != ReasonSelected {
		return true, x.refuseFederation(bound, e, p, reason, fence)
	}
	if !sameFederation(p.Federation, &evidence) {
		return true, x.refuseFederation(bound, e, p, ReasonChanged, fence)
	}
	coordinated, ok := x.Policies.Federation.(federationapi.CleanupCoordination)
	if !ok {
		return true, fmt.Errorf("durable federated cleanup coordination unavailable: %w", kernel.ErrStore)
	}
	// Reserve and save the operation identity together under original current
	// authority and worker ownership, before contacting any participant.
	err = x.withCaller(bound, e.Credential, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(e.Policy.ProjectKey) {
			return kernel.ErrForbidden
		}
		if _, err := x.currentRun(ctx, e); err != nil {
			return fmt.Errorf("execute coordinated cleanup: %w", err)
		}
		return x.publish(ctx, e, p, fence, true, func(ctx context.Context, _ *model.CleanupRun, current *model.CleanupProgress) error {
			id, err := coordinated.Reserve(ctx, evidence, e.RunID, fileID)
			if err != nil {
				return fmt.Errorf("execute coordinated cleanup: %w", err)
			}
			current.CleanupIntentID = id
			return nil
		})
	})
	if err != nil {
		return true, fmt.Errorf("execute coordinated cleanup: %w", err)
	}
	p, err = x.Store.Cleanup().Progress(bound, e.RunID)
	if err != nil {
		return true, fmt.Errorf("read reserved cleanup continuation: %w", errors.Join(work.ErrUnsettled, err))
	}
	reservation, err := coordinated.Prepare(bound, p.CleanupIntentID)
	if err != nil {
		// A failed prepare may already hold some participants. Retain its durable
		// reference until a fenced abort and all participant acknowledgements finish.
		refusal := x.refuseFederation(bound, e, p, ReasonFederationUnknown, fence)
		return true, fmt.Errorf("cleanup exclusion preparation unresolved: %w", errors.Join(work.ErrUnsettled, err, refusal))
	}
	if reason := s.federationDownloadReason(reservation.Evidence); reason != ReasonSelected {
		err = x.refuseFederation(bound, e, p, reason, fence)
		return true, fmt.Errorf("cleanup exclusion retained recent use: %w", errors.Join(work.ErrUnsettled, err))
	}
	s.permit = federatedPermit{federation: coordinated, evidence: reservation.Evidence, version: p.Version, reservation: reservation}
	previous := fence
	fence = func(ctx context.Context) error {
		if err := previous(ctx); err != nil {
			return fmt.Errorf("execute coordinated cleanup: %w", err)
		}
		return coordinated.Fresh(ctx, reservation.Evidence)
	}
	err = x.withCaller(bound, e.Credential, func(ctx context.Context, authority Policy) error {
		return x.authorizedStep(ctx, e, p, s, fence, authority)
	})
	if err != nil {
		return true, x.federatedNativeError(bound, e, p, previous, err)
	}

	return true, nil
}

func (x Executor) federatedNativeError(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error, err error) error {
	var stop *stopRequired
	var authority *authorityRefusal
	var publication *executionRefusal
	if errors.As(err, &stop) || errors.As(err, &authority) || errors.As(err, &publication) || ctx.Err() != nil || errors.Is(err, kernel.ErrBadCredentials) ||
		errors.Is(err, kernel.ErrUnauthenticated) ||
		errors.Is(err, kernel.ErrPasswordExpired) {
		return fmt.Errorf("cleanup control outcome requires settlement: %w", errors.Join(work.ErrUnsettled, err))
	}
	if errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrForbidden) {
		// This records only the normal refusal/report outcome. The durable ID is
		// retained; recovery aborts/releases it before advancing to another effect.
		refusal := x.refuseVersion(ctx, e, p, fence, err)
		return fmt.Errorf("cleanup refusal requires exclusion settlement: %w", errors.Join(work.ErrUnsettled, err, refusal))
	}
	return fmt.Errorf("cleanup native fault requires exclusion settlement: %w", errors.Join(work.ErrUnsettled, &federatedEffectFailure{cause: err}))
}

func (x Executor) refuseFederation(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, reason Reason, fence func(context.Context) error) error {
	return x.withCaller(ctx, e.Credential, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(e.Policy.ProjectKey) {
			return &authorityRefusal{kernel.ErrForbidden}
		}
		if _, err := x.currentRun(ctx, e); err != nil {
			return fmt.Errorf("execute coordinated cleanup: %w", err)
		}
		return x.publish(ctx, e, p, fence, true, func(ctx context.Context, _ *model.CleanupRun, current *model.CleanupProgress) error {
			current.Phase, current.Reason = model.CleanupSkipping, string(reason)
			return x.versionOutcome(ctx, e, p, model.CleanupSkipped, string(reason))
		})
	})
}

// settleFederated runs before terminal/stop/authority branches. Recovery only
// terminates an old destructive operation; it does not perform user effects or
// require a revoked user's credential to restore ordinary member availability.
func (x Executor) settleFederated(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error) error {
	coordinated, ok := x.Policies.Federation.(federationapi.CleanupCoordination)
	if !ok {
		return fmt.Errorf("cleanup coordinator missing: %w", work.ErrUnsettled)
	}
	err := x.Store.Tx(ctx, func(ctx context.Context) error {
		current, err := x.Store.Cleanup().Progress(ctx, e.RunID)
		if err != nil {
			return fmt.Errorf("execute coordinated cleanup: %w", err)
		}
		if current.Revision != p.Revision || current.CleanupIntentID != p.CleanupIntentID {
			return kernel.ErrConflict
		}
		if err := coordinated.Abort(ctx, p.CleanupIntentID); err != nil {
			return fmt.Errorf("execute coordinated cleanup: %w", err)
		}
		if err := tx.CheckPublication(ctx); err != nil {
			return fmt.Errorf("execute coordinated cleanup: %w", err)
		}
		return tx.BeforeCommit(ctx, fence)
	})
	if err == nil {
		err = coordinated.Finish(ctx, p.CleanupIntentID)
	}
	if err == nil {
		err = x.Store.Tx(ctx, func(ctx context.Context) error {
			current, err := x.Store.Cleanup().Progress(ctx, e.RunID)
			if err != nil {
				return fmt.Errorf("execute coordinated cleanup: %w", err)
			}
			if current.Revision != p.Revision || current.CleanupIntentID != p.CleanupIntentID {
				return kernel.ErrConflict
			}
			current.CleanupIntentID = ""
			current.Revision++
			if err := x.Store.Cleanup().AdvanceProgress(ctx, p.Revision, current); err != nil {
				return fmt.Errorf("execute coordinated cleanup: %w", err)
			}
			if err := tx.CheckPublication(ctx); err != nil {
				return fmt.Errorf("execute coordinated cleanup: %w", err)
			}
			return tx.BeforeCommit(ctx, fence)
		})
	}
	if err != nil {
		return fmt.Errorf("cleanup exclusion recovery pending: %w", errors.Join(work.ErrUnsettled, err))
	}
	return nil
}
