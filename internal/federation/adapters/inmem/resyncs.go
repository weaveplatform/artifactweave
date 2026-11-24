package inmem

import (
	"cmp"
	"context"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Resyncs returns the records of receivers behind an origin's pruned floor
// (decision 0021, Ordered outbox).
func (s *Store) Resyncs() port.StreamResyncStore { return resyncStore{s} }

type resyncStore struct{ s *Store }

type resyncKey struct{ origin, receiver, stream string }

func (r resyncStore) t() *memtx.Table[resyncKey, model.StreamResync] {
	return memtx.TableOf[resyncKey, model.StreamResync](r.s.Store, tResyncs)
}

func (r resyncStore) Flag(ctx context.Context, f model.StreamResync) error {
	return r.s.Do(ctx, func() error {
		r.t().Put(resyncKey{f.Origin, f.Receiver, f.Stream}, kernel.Normalise(f))
		return nil
	})
}

func (r resyncStore) Resyncs(ctx context.Context, instanceID string) ([]model.StreamResync, error) {
	var out []model.StreamResync
	err := r.s.Do(ctx, func() error {
		out = r.t().All(func(_ resyncKey, v model.StreamResync) bool {
			return instanceID == "" || v.Origin == instanceID || v.Receiver == instanceID
		}, func(a, b model.StreamResync) int {
			return cmp.Or(cmp.Compare(a.Origin, b.Origin), cmp.Compare(a.Receiver, b.Receiver),
				cmp.Compare(a.Stream, b.Stream))
		})
		return nil
	})
	return out, err
}

func (r resyncStore) Clear(ctx context.Context, origin, receiver, stream string) error {
	return r.s.Do(ctx, func() error {
		r.t().Del(resyncKey{origin, receiver, stream})
		return nil
	})
}

func (r resyncStore) DeletePeer(ctx context.Context, instanceID string) error {
	return r.s.Do(ctx, func() error {
		r.t().DeleteWhere(func(k resyncKey, _ model.StreamResync) bool {
			return k.origin == instanceID || k.receiver == instanceID
		})
		return nil
	})
}
