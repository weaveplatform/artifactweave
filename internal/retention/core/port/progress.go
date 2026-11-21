package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// CleanupExecutionStore joins artifact effects, progress and the work lease.
type CleanupExecutionStore interface {
	Store
	WorkQueue() work.WorkQueue
	InTx(context.Context) bool
}

// CleanupEntryQuery bounds retained outcome and fingerprint reads. Zero
// VersionID, empty Kind or empty State means no filter for that dimension.
type CleanupEntryQuery struct {
	RunID, VersionID int64
	Kind             model.CleanupEntryKind
	State            model.CleanupEntryState
}

// CleanupProgressStore joins the shared unit of work. Writes require that
// transaction; progression is version-fenced and snapshots cannot be replaced.
type CleanupProgressStore interface {
	CreateProgress(context.Context, model.CleanupProgress) error
	Progress(context.Context, int64) (model.CleanupProgress, error)
	AdvanceProgress(context.Context, int64, model.CleanupProgress) error
	CreateEntry(context.Context, model.CleanupEntry) (bool, error)
	Entry(context.Context, int64, model.CleanupEntryKind, int64) (model.CleanupEntry, error)
	AdvanceEntry(context.Context, int64, model.CleanupEntry) error
	// Entries orders by kind/entity ID. A fingerprint scan uses item kind and
	// follows every continuation, without retaining a complete version in RAM.
	Entries(context.Context, CleanupEntryQuery, kernel.Page) ([]model.CleanupEntry, string, error)
	// PendingEntry selects one file, non-leads first, then ascending lead
	// rank/tie/ID, so a format's chosen single lead is removed last.
	PendingEntry(context.Context, int64, int64) (model.CleanupEntry, error)
}
