package sqlcommon

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Replays implements store.Store.
func (s *Store) Replays() store.ReplayStore { return replayStore{s} }

type replayStore struct{ s *Store }

func (s replayStore) Do(
	ctx context.Context,
	r store.ReplayRequest,
	effect func(context.Context) (int64, error),
) (store.ReplayReceipt, error) {
	var out store.ReplayReceipt
	if err := r.Validate(); err != nil {
		return out, fmt.Errorf("replay: %w", err)
	}
	if effect == nil {
		return out, fmt.Errorf("replay effect is required: %w", store.ErrInvalid)
	}
	err := s.s.Tx(ctx, func(ctx context.Context) error {
		cols := []string{"scope", "key_hash", "fingerprint", "resource_id", "expires_at"}
		_, err := s.s.Exec(
			ctx,
			"claim replay",
			InsertIgnore("api_replays", cols, []string{"scope", "key_hash"}),
			r.Scope,
			r.Key,
			r.Fingerprint,
			0,
			0,
		)
		if err != nil {
			return err
		}
		var fingerprint string
		var expires int64
		query := "SELECT fingerprint, resource_id, expires_at FROM api_replays " +
			"WHERE scope = ? AND key_hash = ? " + s.s.Dialect().ForUpdate
		if err := s.s.Q(ctx).QueryRowContext(ctx, s.s.Dialect().Rebind(query), r.Scope, r.Key).
			Scan(&fingerprint, &out.ResourceID, &expires); err != nil {
			return s.s.Wrap("read replay", err)
		}
		if expires > r.Now.UnixMicro() {
			if fingerprint != r.Fingerprint {
				return store.ErrReplayMismatch
			}
			out.Expires, out.Replayed = time.UnixMicro(expires).UTC(), true
			return nil
		}
		id, err := effect(ctx)
		if err != nil {
			return err
		}
		if id <= 0 {
			return fmt.Errorf("replay resource ID must be positive: %w", store.ErrInvalid)
		}
		_, err = s.s.Exec(
			ctx,
			"complete replay",
			"UPDATE api_replays SET fingerprint = ?, resource_id = ?, expires_at = ? WHERE scope = ? AND key_hash = ?",
			r.Fingerprint,
			id,
			r.Expires.UnixMicro(),
			r.Scope,
			r.Key,
		)
		out = store.ReplayReceipt{ResourceID: id, Expires: store.Normalise(r.Expires)}
		return err
	})
	return out, err
}

func (s replayStore) Prune(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit < 1 || limit > store.MaxLimit {
		return 0, fmt.Errorf("replay prune limit: %w", store.ErrInvalid)
	}
	return s.s.Exec(
		ctx,
		"prune replays",
		"DELETE FROM api_replays WHERE expires_at <= ? AND (scope, key_hash) IN ("+
			"SELECT scope, key_hash FROM api_replays WHERE expires_at <= ? ORDER BY expires_at, scope, key_hash LIMIT ?)",
		now.UnixMicro(),
		now.UnixMicro(),
		limit,
	)
}
