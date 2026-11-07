package idempotency

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// ErrReplayMismatch means an unexpired key was reused with different intent.
var ErrReplayMismatch = errors.New("store: idempotency key has different intent")

// ReplayRequest identifies one protected effect. Scope, Key and Fingerprint
// are lowercase SHA-256 hex digests, never credentials, raw keys or request
// bodies. Scope includes the authenticated credential and canonical resource.
// Now and Expires use the caller's clock; replicas must have synchronized clocks.
type ReplayRequest struct {
	Scope, Key, Fingerprint string
	Now, Expires            time.Time
}

// Validate checks the fixed storage bounds and a positive retention interval.
// A request out of bounds gives an error that wraps kernel.ErrInvalid.
func (r ReplayRequest) Validate() error {
	for _, v := range []string{r.Scope, r.Key, r.Fingerprint} {
		b, err := hex.DecodeString(v)
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != v {
			return fmt.Errorf("replay digests must be lowercase SHA-256 hex: %w", kernel.ErrInvalid)
		}
	}
	if r.Now.IsZero() || r.Expires.UnixMicro() <= r.Now.UnixMicro() {
		return fmt.Errorf("replay retention must be positive: %w", kernel.ErrInvalid)
	}
	return nil
}

// ReplayReceipt identifies the committed resource. It deliberately has no
// response-body field: secrets must not enter a generic replay cache.
type ReplayReceipt struct {
	ResourceID int64
	Expires    time.Time
	Replayed   bool
}

// ReplayStore deduplicates local transactional effects. It is not an outbox
// for external calls. An expired key may perform a new effect.
type ReplayStore interface {
	// Do serializes concurrent calls for (Scope, Key), checks the fingerprint,
	// and runs effect only for a new/expired key. effect must use its supplied
	// context for all mutations and return a positive stable resource ID.
	// The receipt and mutations commit together; any error rolls both back.
	// A nested call joins the metadata store's outer transaction. Replays do
	// not call effect. Waiting obeys context cancellation; there is no
	// committed pending state to recover after a process crash. Callers must
	// reauthorize the returned resource, including on replay, before their
	// outer transaction commits. A request that Validate refuses, a nil
	// effect and a resource ID that is not positive are kernel.ErrInvalid; an
	// unexpired key with another fingerprint is ErrReplayMismatch.
	Do(
		ctx context.Context,
		request ReplayRequest,
		effect func(context.Context) (int64, error),
	) (ReplayReceipt, error)
	// Prune removes at most limit expired receipts (1..kernel.MaxLimit; any
	// other limit is kernel.ErrInvalid). Retention ends at expiry, whether or
	// not the physical row has been pruned.
	Prune(ctx context.Context, now time.Time, limit int) (int64, error)
}
