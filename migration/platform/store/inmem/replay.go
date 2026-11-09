package inmem

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/idempotency"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// Replays returns the API replay store, which commits an effect and its
// receipt in one transaction (decision 0043).
func (s *Store) Replays() idempotency.ReplayStore { return replayStore{s} }

type (
	replayStore  struct{ s *Store }
	replayKey    struct{ scope, key string }
	replayRecord struct {
		fingerprint string
		receipt     idempotency.ReplayReceipt
	}
)

func (s replayStore) rows() *memtx.Table[replayKey, replayRecord] {
	return memtx.TableOf[replayKey, replayRecord](s.s.Store, "api_replays")
}

func (s replayStore) Do(
	ctx context.Context,
	r idempotency.ReplayRequest,
	effect func(context.Context) (int64, error),
) (idempotency.ReplayReceipt, error) {
	var out idempotency.ReplayReceipt
	if err := r.Validate(); err != nil {
		return out, err //nolint:wrapcheck // Validate's error names the replay
	}
	if effect == nil {
		return out, fmt.Errorf("replay effect is required: %w", kernel.ErrInvalid)
	}
	err := s.s.Tx(ctx, func(ctx context.Context) error {
		key := replayKey{r.Scope, r.Key}
		cur, ok := s.rows().Rows[key]
		if ok && cur.receipt.Expires.UnixMicro() > r.Now.UnixMicro() {
			if cur.fingerprint != r.Fingerprint {
				return idempotency.ErrReplayMismatch
			}
			out = cur.receipt
			out.Replayed = true
			return nil
		}
		id, err := effect(ctx)
		if err != nil {
			return err
		}
		if id <= 0 {
			return fmt.Errorf("replay resource ID must be positive: %w", kernel.ErrInvalid)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		out = idempotency.ReplayReceipt{ResourceID: id, Expires: kernel.Normalise(r.Expires)}
		s.rows().Rows[key] = replayRecord{r.Fingerprint, out}
		return nil
	})
	return out, err
}

func (s replayStore) Prune(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit < 1 || limit > kernel.MaxLimit {
		return 0, fmt.Errorf("replay prune limit: %w", kernel.ErrInvalid)
	}
	var n int64
	err := s.s.Do(ctx, func() error {
		for key, v := range s.rows().Rows {
			if v.receipt.Expires.UnixMicro() <= now.UnixMicro() {
				delete(s.rows().Rows, key)
				n++
				if n == int64(limit) {
					break
				}
			}
		}
		return nil
	})
	return n, err
}
