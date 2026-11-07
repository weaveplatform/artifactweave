package inmem

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func (s *Store) Replays() store.ReplayStore { return replayStore{s} }

type (
	replayStore  struct{ s *Store }
	replayKey    struct{ scope, key string }
	replayRecord struct {
		fingerprint string
		receipt     store.ReplayReceipt
	}
)

func (s replayStore) rows() *memtx.Table[replayKey, replayRecord] {
	return memtx.TableOf[replayKey, replayRecord](s.s.Store, "api_replays")
}

func (s replayStore) Do(
	ctx context.Context,
	r store.ReplayRequest,
	effect func(context.Context) (int64, error),
) (store.ReplayReceipt, error) {
	var out store.ReplayReceipt
	if err := r.Validate(); err != nil {
		return out, err
	}
	if effect == nil {
		return out, fmt.Errorf("replay effect is required: %w", store.ErrInvalid)
	}
	err := s.s.Tx(ctx, func(ctx context.Context) error {
		key := replayKey{r.Scope, r.Key}
		cur, ok := s.rows().Rows[key]
		if ok && cur.receipt.Expires.UnixMicro() > r.Now.UnixMicro() {
			if cur.fingerprint != r.Fingerprint {
				return store.ErrReplayMismatch
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
			return fmt.Errorf("replay resource ID must be positive: %w", store.ErrInvalid)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		out = store.ReplayReceipt{ResourceID: id, Expires: store.Normalise(r.Expires)}
		s.rows().Rows[key] = replayRecord{r.Fingerprint, out}
		return nil
	})
	return out, err
}

func (s replayStore) Prune(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit < 1 || limit > store.MaxLimit {
		return 0, fmt.Errorf("replay prune limit: %w", store.ErrInvalid)
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
