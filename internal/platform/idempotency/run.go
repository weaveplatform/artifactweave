package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

const (
	// Retention is how long a receipt holds its key: 24 hours after the
	// first successful request's admission (decision 0043).
	Retention = 24 * time.Hour
	// Budget bounds a keyed request, its wait for a concurrent original
	// included, when Replay.Budget is zero (decision 0043).
	Budget = 30 * time.Second
	// PruneLimit is how many expired receipts each keyed request removes
	// before it runs, so that cleanup keeps pace with keyed traffic without
	// a sweeper (decision 0043).
	PruneLimit = 100
)

// Replay runs keyed requests once each (decision 0043, "Replay for keyed
// operations"). Its zero value is not usable: Store and Tx must be the
// receipts and the unit of work of one metadata store, so that a claim, the
// effect and the receipt share a transaction.
type Replay struct {
	// Store keeps the receipts.
	Store ReplayStore
	// Tx is the metadata store's unit of work, which every store call the
	// effect makes with its context joins.
	Tx tx.Runner
	// Now supplies the time a keyed request is admitted at; nil means
	// time.Now.
	Now func() time.Time
	// Budget bounds each request; zero means the constant Budget. Tests
	// shorten it.
	Budget time.Duration
}

// Effect is the work of one keyed request.
type Effect struct {
	// Run makes the effect through the context it is given and returns the
	// positive, stable ID of the resource it created or changed. It runs
	// only for a key that has no unexpired receipt.
	Run func(ctx context.Context) (int64, error)
	// Reload locks and loads the resource the receipt names and rechecks
	// the current credential and permissions. It runs after Run and on
	// every replay, in the same transaction, so a receipt commits only once
	// its resource has been confirmed, and a replay never outlives the
	// access that made it.
	Reload func(ctx context.Context, receipt ReplayReceipt) error
}

func (p Replay) now() time.Time {
	if p.Now == nil {
		return time.Now().UTC()
	}
	return p.Now().UTC()
}

func (p Replay) budget() time.Duration {
	if p.Budget <= 0 {
		return Budget
	}
	return p.Budget
}

// Do prunes at most PruneLimit expired receipts, then runs e under r in one
// transaction: the claim, e.Run for a new or expired key, e.Reload, and the
// receipt commit together or roll back together. The whole of it, a wait
// for a concurrent original included, is bounded by ctx and the budget;
// when either ends first the error wraps context.DeadlineExceeded, which
// leaves the caller unable to tell whether a concurrent original committed.
// A request Validate refuses and a nil Run or Reload are kernel.ErrInvalid;
// an unexpired key with another fingerprint is ErrReplayMismatch. Called
// inside a transaction of the same store, Do joins it, and the outer commit
// decides for both.
func (p Replay) Do(ctx context.Context, r ReplayRequest, e Effect) (ReplayReceipt, error) {
	var receipt ReplayReceipt
	if err := r.Validate(); err != nil {
		return receipt, fmt.Errorf("idempotency: %w", err)
	}
	if e.Run == nil || e.Reload == nil {
		return receipt, fmt.Errorf(
			"idempotency: an effect and its reload are required: %w",
			kernel.ErrInvalid,
		)
	}
	ctx, cancel := context.WithTimeout(ctx, p.budget())
	defer cancel()
	err := p.do(ctx, r, e, &receipt)
	// A store or handler that maps its error loses the deadline behind it;
	// the context still knows, and the caller's 503 depends on it.
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) &&
		!errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("%w: %w", err, context.DeadlineExceeded)
	}
	return receipt, err
}

func (p Replay) do(ctx context.Context, r ReplayRequest, e Effect, receipt *ReplayReceipt) error {
	if _, err := p.Store.Prune(ctx, r.Now, PruneLimit); err != nil {
		return fmt.Errorf("idempotency: prune receipts: %w", err)
	}
	err := p.Tx.Tx(ctx, func(ctx context.Context) error {
		var err error
		*receipt, err = p.Store.Do(ctx, r, e.Run)
		if err != nil {
			return err //nolint:wrapcheck // the effect's own error, which the caller maps
		}
		return e.Reload(ctx, *receipt)
	})
	return err //nolint:wrapcheck // the effect's and reload's own errors, which the caller maps
}
