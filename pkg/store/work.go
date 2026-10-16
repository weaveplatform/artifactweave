package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Bounds on a work item, so the queue stays a queue of references to work
// rather than a store of its data (decision 0033).
const (
	MaxWorkKind    = 64
	MaxWorkKey     = 1024
	MaxWorkPayload = 64 << 10
)

// WorkQueue persists asynchronous derived work (decision 0033): one item per
// kind and key, run by one worker at a time. Every method is given the time
// it acts at, so no backend reads a clock.
type WorkQueue interface {
	// Enqueue asks for the work kind names for key, with payload (a small
	// JSON document, or empty). Called inside the caller's transaction, it
	// commits or rolls back with the change that needs the work.
	//   - A key with no item gets a pending one, due now.
	//   - A pending key keeps its schedule and takes the new payload.
	//   - A leased key takes the new payload and is marked to run once more
	//     when its current run ends.
	//   - A parked key is pending again from now, its attempts reset.
	// ErrInvalid for an empty or overlong kind or key, or a payload that is
	// not JSON or exceeds MaxWorkPayload.
	Enqueue(ctx context.Context, kind, key string, payload []byte, now time.Time) error
	// Lease takes up to req.Limit due items of req.Kinds (every kind when
	// empty) for req.Holder until req.Until: pending items from their
	// NotBefore on, and leased items whose lease ran out, the longest due
	// first. Each taken item's Generation and Attempts go up by one and its
	// DirtySince clears, since the new run covers that work. Concurrent
	// callers never take the same item.
	Lease(ctx context.Context, req WorkLease) ([]types.WorkItem, error)
	// LeaseKey takes the item of kind and key, as Lease would, when it is
	// pending (whatever its NotBefore) or its lease ran out, so a reader
	// can bring derived data up to date before using it. It returns the
	// item as it stands and whether it was taken. ErrNotFound when there is
	// no item.
	LeaseKey(ctx context.Context, kind, key string, req WorkLease) (types.WorkItem, bool, error)
	// Extend moves the lease of the run with generation gen to until.
	// ErrConflict when that run no longer holds the item.
	Extend(ctx context.Context, kind, key string, gen int64, until, now time.Time) error
	// Complete ends the run with generation gen successfully. The item is
	// deleted, or, when work was asked for again during the run, pending
	// again from now with its attempts reset. ErrConflict when the run no
	// longer holds the item.
	Complete(ctx context.Context, kind, key string, gen int64, now time.Time) error
	// Fail ends the run with generation gen unsuccessfully: pending again
	// from f.RetryAt, or parked when f.Park and no work was asked for during
	// the run. ErrConflict when the run no longer holds the item.
	Fail(ctx context.Context, kind, key string, gen int64, f WorkFailure) error
	// Release hands back the run with generation gen unfinished, as a
	// stopping worker does: pending again from now, without counting the
	// attempt. ErrConflict when the run no longer holds the item.
	Release(ctx context.Context, kind, key string, gen int64, now time.Time) error
	// Get returns the item of kind and key; ErrNotFound when there is none.
	Get(ctx context.Context, kind, key string) (types.WorkItem, error)
	// Stats counts each kind's items by state, ordered by kind.
	Stats(ctx context.Context) ([]types.WorkStats, error)
}

// WorkLease is what a worker asks the queue for.
type WorkLease struct {
	// Kinds limits Lease to these kinds; empty takes any.
	Kinds  []string
	Holder string
	// Limit is the most items Lease takes; LeaseKey ignores it.
	Limit int
	// Until is when the lease runs out unless extended; Now is when it is
	// taken.
	Until, Now time.Time
}

// WorkFailure is how a run ended unsuccessfully.
type WorkFailure struct {
	Error string
	// RetryAt is when the item is due again unless parked.
	RetryAt time.Time
	// Park stops retrying until the key is enqueued again.
	Park bool
	Now  time.Time
}

// ValidateWork checks an item to enqueue against the queue's bounds.
func ValidateWork(kind, key string, payload []byte) error {
	switch {
	case kind == "" || key == "":
		return fmt.Errorf("work item %q/%q: kind and key are required: %w", kind, key, ErrInvalid)
	case len(kind) > MaxWorkKind || len(key) > MaxWorkKey:
		return fmt.Errorf("work item %q: kind or key too long: %w", kind, ErrInvalid)
	case len(payload) > MaxWorkPayload:
		return fmt.Errorf("work item %s/%s: payload of %d bytes is over %d: %w",
			kind, key, len(payload), MaxWorkPayload, ErrInvalid)
	case len(payload) > 0 && !json.Valid(payload):
		return fmt.Errorf("work item %s/%s: payload is not JSON: %w", kind, key, ErrInvalid)
	}
	return nil
}

// ValidateLease checks a lease request.
func ValidateLease(req WorkLease) error {
	if req.Holder == "" || req.Until.IsZero() || !req.Until.After(req.Now) {
		return fmt.Errorf("work lease: a holder and an expiry after now are required: %w",
			ErrInvalid)
	}
	return nil
}
