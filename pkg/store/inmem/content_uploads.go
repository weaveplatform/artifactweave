package inmem

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Uploads implements store.Store.
func (s *Store) Uploads() store.UploadStore { return uploads{s} }

// --- Uploads ---------------------------------------------------------------

type uploads struct{ s *Store }

func (u uploads) t() *table[string, store.Upload] {
	return tableOf[string, store.Upload](u.s, "uploads")
}

func (u uploads) Put(ctx context.Context, up store.Upload) error {
	if up.ID == "" {
		return fmt.Errorf("put upload: id is required: %w", store.ErrInvalid)
	}
	return u.s.do(ctx, func() error {
		if len(up.State) == 0 {
			up.State = nil
		}
		u.t().put(up.ID, up)
		return nil
	})
}

func (u uploads) Get(ctx context.Context, id string) (store.Upload, error) {
	var out store.Upload
	err := u.s.do(ctx, func() error {
		var ok bool
		if out, ok = u.t().get(id); !ok {
			return notFound("upload", id)
		}
		return nil
	})
	return out, err
}

func (u uploads) Delete(ctx context.Context, id string) error {
	return u.s.do(ctx, func() error {
		if !u.t().del(id) {
			return notFound("upload", id)
		}
		return nil
	})
}

// lastActive is when a session was last touched for staleness, in the SQL
// column's terms: Updated, else Started, else the epoch.
func lastActive(up store.Upload) int64 {
	switch {
	case !up.Updated.IsZero():
		return up.Updated.UnixMicro()
	case !up.Started.IsZero():
		return up.Started.UnixMicro()
	default:
		return 0
	}
}

func (u uploads) Stale(ctx context.Context, before time.Time, limit int) ([]store.Upload, error) {
	var out []store.Upload
	err := u.s.do(ctx, func() error {
		cutoff := before.UnixMicro()
		out = u.t().all(func(_ string, up store.Upload) bool { return lastActive(up) < cutoff },
			func(a, b store.Upload) int {
				return cmp.Or(cmp.Compare(lastActive(a), lastActive(b)), cmp.Compare(a.ID, b.ID))
			})
		out = out[:min(len(out), limitOr(limit))]
		return nil
	})
	return out, err
}
