package replication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// TargetWorkKind reserves a target lifetime within the existing work queue.
// It has no handler/payload and is never a logical run. Release retains its row
// and monotonically increasing generation, preventing deletion/recreation ABA.
const TargetWorkKind = "replication:target"

// OwnershipStore joins business publication and queue generation checks in the
// same metadata unit of work. It creates no second queue or lease subsystem.
type OwnershipStore interface {
	tx.Runner
	InTx(context.Context) bool
	WorkQueue() work.WorkQueue
}

// Ownership excludes a different manual/scheduled logical run of one target.
// The normal run queue still owns retries, heartbeat and shutdown. A worker
// renews its target reservation while transferring outside transactions.
type Ownership struct {
	Store    OwnershipStore
	Guard    func(context.Context) error
	Now      func() time.Time
	Duration time.Duration
}

// Owner retains the exact run attempt and its target reservation generation.
// Neither descriptor is permission to execute without current configuration.
type Owner struct {
	Run    work.WorkItem
	Target work.WorkItem
}

func (o Ownership) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o Ownership) duration() time.Duration {
	if o.Duration > 0 {
		return o.Duration
	}
	return work.DefaultLease
}

func ownershipHash(value any) string {
	// The caller supplies only strings and integer dispatch facts.
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func validRunOwner(item work.WorkItem) bool {
	return item.Kind != "" && item.Kind != TargetWorkKind && item.Key != "" &&
		item.State == work.WorkLeased && item.LeasedBy != "" && item.Generation > 0
}

// Claim reserves a target only for a currently live queue attempt. A competing
// target owner or expired run rolls back enqueue/lease changes. Call outside a
// metadata transaction; Guard precedes target-reservation and run queue locks.
func (o Ownership) Claim(ctx context.Context, item work.WorkItem, target string) (Owner, error) {
	if o.Store == nil || o.Guard == nil || o.Store.InTx(ctx) || !validRunOwner(item) || target == "" {
		return Owner{}, fmt.Errorf("invalid replication dispatch ownership: %w", kernel.ErrInvalid)
	}
	owner := Owner{Run: item}
	err := o.Store.Tx(ctx, func(ctx context.Context) error {
		if err := o.Guard(ctx); err != nil {
			return fmt.Errorf("guard replication target: %w", err)
		}
		now := o.now()
		key := ownershipHash(target)
		if err := o.Store.WorkQueue().Enqueue(ctx, TargetWorkKind, key, nil, now); err != nil {
			return fmt.Errorf("reserve replication target: %w", err)
		}
		holder := ownershipHash([]any{item.Kind, item.Key, item.Generation, item.LeasedBy})
		leased, taken, err := o.Store.WorkQueue().LeaseKey(ctx, TargetWorkKind, key, work.WorkLease{
			Holder: holder, Now: now, Until: now.Add(o.duration()),
		})
		if err != nil {
			return fmt.Errorf("lease replication target: %w", err)
		}
		if !taken {
			return fmt.Errorf("replication target has another run: %w", kernel.ErrConflict)
		}
		owner.Target = leased
		return o.Fence(ctx, owner)
	})
	if err != nil {
		return Owner{}, fmt.Errorf("claim replication target: %w", err)
	}
	return owner, nil
}

// Fence is last in a publication transaction, after every business, progress,
// status and event effect. Target reservation precedes the run queue row. Read
// the clock after both locks; neither an expired reservation nor an old run
// generation can publish even if no successor has claimed it yet.
func (o Ownership) Fence(ctx context.Context, owner Owner) error {
	if o.Store == nil || !o.Store.InTx(ctx) || !validRunOwner(owner.Run) ||
		owner.Target.Kind != TargetWorkKind || owner.Target.Key == "" || owner.Target.Generation < 1 {
		return fmt.Errorf("replication publication needs transactional ownership: %w", kernel.ErrInvalid)
	}
	targetUntil, err := o.Store.WorkQueue().GuardLease(ctx, owner.Target.Kind, owner.Target.Key, owner.Target.Generation)
	if err != nil {
		return fmt.Errorf("guard replication target reservation: %w", err)
	}
	runUntil, err := o.Store.WorkQueue().GuardLease(ctx, owner.Run.Kind, owner.Run.Key, owner.Run.Generation)
	if err != nil {
		return fmt.Errorf("guard replication run attempt: %w", err)
	}
	now := o.now()
	if !targetUntil.After(now) || !runUntil.After(now) {
		return fmt.Errorf("replication execution ownership expired: %w", kernel.ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("replication ownership context ended: %w", err)
	}
	return nil
}

// Renew retains both current generations while extending the target lease.
// It cannot resurrect an expired target reservation. The caller must cancel
// network work and refuse further success publication after any renewal failure.
func (o Ownership) Renew(ctx context.Context, owner Owner) error {
	if o.Store == nil || o.Guard == nil || o.Store.InTx(ctx) {
		return fmt.Errorf("replication renewal needs an external context and guard: %w", kernel.ErrInvalid)
	}
	err := o.Store.Tx(ctx, func(ctx context.Context) error {
		if err := o.Guard(ctx); err != nil {
			return fmt.Errorf("guard replication renewal: %w", err)
		}
		if err := o.Fence(ctx, owner); err != nil {
			return err
		}
		now := o.now()
		if err := o.Store.WorkQueue().Extend(ctx, owner.Target.Kind, owner.Target.Key, owner.Target.Generation, now.Add(o.duration()), now); err != nil {
			return fmt.Errorf("extend replication target reservation: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("renew replication target: %w", err)
	}
	return nil
}

// Release gives up only this reservation generation. The queue row is retained
// so an expired attempt cannot match a newly created generation-one reservation.
func (o Ownership) Release(ctx context.Context, owner Owner) error {
	if o.Store == nil || owner.Target.Kind != TargetWorkKind || owner.Target.Key == "" || owner.Target.Generation < 1 {
		return fmt.Errorf("replication reservation is unavailable: %w", kernel.ErrInvalid)
	}
	if err := o.Store.WorkQueue().Release(ctx, owner.Target.Kind, owner.Target.Key, owner.Target.Generation, o.now()); err != nil {
		return fmt.Errorf("release replication target reservation: %w", err)
	}
	return nil
}
