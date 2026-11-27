package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

// CleanupIntentStore keeps irreversible coordinator outcomes independently of
// worker and repository deletion. All publication joins the shared unit of work.
type CleanupIntentStore interface {
	// Create requires an enclosing transaction and is idempotent only for the
	// same immutable operation binding. It cannot resurrect an old operation.
	Create(context.Context, model.CleanupIntent) error
	// Read locks an existing row while the caller is in a transaction.
	Read(context.Context, string) (model.CleanupIntent, error)
	// Advance requires an enclosing transaction, the current revision and a
	// permitted monotonic state transition.
	Advance(context.Context, int64, model.CleanupIntent) error
}
