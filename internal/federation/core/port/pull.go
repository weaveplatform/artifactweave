package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PullStore is the shared metadata unit of work for execution bookkeeping.
// Configuration-only services need not depend on progress or execution.
type PullStore interface {
	ConfigurationStore
	PullProgress() PullProgressStore
	InTx(context.Context) bool
}

// PullProgressStore owns private logical runs independently of queue rows and
// the public status-only replication_runs table. Records survive target removal
// so an old run ID cannot silently bind to a recreated target/configuration.
// Reading retained evidence grants no current execution or content authority.
type PullProgressStore interface {
	// Create inserts a fresh run or returns its existing lifetime only when
	// the immutable binding is identical. Different binding returns ErrConflict;
	// retries preserve original identity, timestamps, version and progress.
	Create(context.Context, string, model.PullBinding, time.Time) (model.PullRun, error)
	// Run reads the exact lifetime, locking it in an enclosing transaction.
	Run(context.Context, model.PullReference) (model.PullRun, error)
	// Advance requires an enclosing transaction, a current expected version and
	// a valid monotonic transition. Zero/stale matches return ErrConflict.
	// A completed traversal must have no incomplete item records.
	Advance(context.Context, int64, model.PullRun) error
	// CreateItem records an immutable operation intent once, or returns existing
	// evidence for the same intent. Different source/target facts conflict.
	// It locks the parent run before the item and rejects terminal parents.
	CreateItem(context.Context, model.PullItem) (model.PullItem, error)
	Item(context.Context, model.PullReference, model.PullAction, string) (model.PullItem, error)
	// AdvanceItem joins the caller's transaction and version-fences a monotonic
	// evidence transition, never changing the source or original cache intent.
	AdvanceItem(context.Context, int64, model.PullItem) error
	// Items is bounded and orders by an opaque operation key. Pending selects
	// unfinished intents for recovery before advancing source/target cursors.
	// An empty continuation ends the walk, including an empty matching page.
	Items(context.Context, model.PullReference, bool, kernel.Page) ([]model.PullItem, string, error)
}
