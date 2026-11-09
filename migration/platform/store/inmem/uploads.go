package inmem

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/session"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// Uploads returns the upload session store (decision 0027).
func (s *Store) Uploads() session.UploadStore { return uploads{s} }

type uploads struct{ s *Store }

func (u uploads) t() *memtx.Table[string, session.Upload] {
	return memtx.TableOf[string, session.Upload](u.s.Store, "uploads")
}

func (u uploads) Put(ctx context.Context, up session.Upload) error {
	if up.ID == "" {
		return fmt.Errorf("put upload: id is required: %w", kernel.ErrInvalid)
	}
	return u.s.Do(ctx, func() error {
		if len(up.State) == 0 {
			up.State = nil
		}
		u.t().Put(up.ID, up)
		return nil
	})
}

func (u uploads) Get(ctx context.Context, id string) (session.Upload, error) {
	var out session.Upload
	err := u.s.Do(ctx, func() error {
		var ok bool
		if out, ok = u.t().Get(id); !ok {
			return memtx.NotFound("upload", id)
		}
		return nil
	})
	return out, err
}

func (u uploads) Delete(ctx context.Context, id string) error {
	return u.s.Do(ctx, func() error {
		if !u.t().Del(id) {
			return memtx.NotFound("upload", id)
		}
		return nil
	})
}

// lastActive is when a session was last touched for staleness, in the SQL
// column's terms: Updated, else Started, else the epoch.
func lastActive(up session.Upload) int64 {
	switch {
	case !up.Updated.IsZero():
		return up.Updated.UnixMicro()
	case !up.Started.IsZero():
		return up.Started.UnixMicro()
	default:
		return 0
	}
}

func (u uploads) Stale(ctx context.Context, before time.Time, limit int) ([]session.Upload, error) {
	var out []session.Upload
	err := u.s.Do(ctx, func() error {
		cutoff := before.UnixMicro()
		out = u.t().All(func(_ string, up session.Upload) bool { return lastActive(up) < cutoff },
			func(a, b session.Upload) int {
				return cmp.Or(cmp.Compare(lastActive(a), lastActive(b)), cmp.Compare(a.ID, b.ID))
			})
		out = out[:min(len(out), limitOr(limit))]
		return nil
	})
	return out, err
}
