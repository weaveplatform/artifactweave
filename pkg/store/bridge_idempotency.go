// Bridge (P9d): declared in migration/platform/idempotency; PRO-02 deletes it.

package store

import "github.com/weaveplatform/artifactweave/migration/platform/idempotency"

var ErrReplayMismatch = idempotency.ErrReplayMismatch

type (
	ReplayRequest = idempotency.ReplayRequest
	ReplayReceipt = idempotency.ReplayReceipt
	ReplayStore   = idempotency.ReplayStore
)
