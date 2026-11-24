package inmem

import (
	"context"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const tItemStamps = "federation_item_stamps"

// Stamps returns the conflict rule's item records (decision 0021).
func (s *Store) Stamps() port.ItemStampStore { return stampStore{s} }

type stampStore struct{ s *Store }

type stampKey struct{ repo, path string }

func (st stampStore) t() *memtx.Table[stampKey, model.ItemStamp] {
	return memtx.TableOf[stampKey, model.ItemStamp](st.s.Store, tItemStamps)
}

func (st stampStore) Stamp(ctx context.Context, repoKey, path string) (model.ItemStamp, error) {
	var out model.ItemStamp
	err := st.s.Do(ctx, func() error {
		v, ok := st.t().Get(stampKey{repoKey, path})
		if !ok {
			return kernel.ErrNotFound
		}
		out = v
		return nil
	})
	return out, err
}

func (st stampStore) Record(ctx context.Context, s model.ItemStamp) error {
	return st.s.Do(ctx, func() error {
		st.t().Put(stampKey{s.RepoKey, s.Path}, s)
		return nil
	})
}

func (st stampStore) Replace(ctx context.Context, prev *model.Stamp, next model.ItemStamp) (bool, error) {
	var done bool
	err := st.s.Do(ctx, func() error {
		k := stampKey{next.RepoKey, next.Path}
		cur, ok := st.t().Get(k)
		if ok != (prev != nil) || ok && cur.Stamp != *prev {
			return nil
		}
		st.t().Put(k, next)
		done = true
		return nil
	})
	return done, err
}

func byPath(a, b model.ItemStamp) int { return strings.Compare(a.Path, b.Path) }

func (st stampStore) PendingOn(ctx context.Context, repoKey, sha256 string) ([]model.ItemStamp, error) {
	var out []model.ItemStamp
	err := st.s.Do(ctx, func() error {
		out = st.t().All(func(k stampKey, v model.ItemStamp) bool {
			return k.repo == repoKey && v.SHA256 == sha256 && len(v.Pending) > 0
		}, byPath)
		return nil
	})
	return out, err
}

func (st stampStore) Holds(ctx context.Context, repoKey, sha256 string) (bool, error) {
	var ok bool
	err := st.s.Do(ctx, func() error {
		ok = len(st.t().First(func(k stampKey, v model.ItemStamp) bool {
			return k.repo == repoKey && v.SHA256 == sha256 && v.Live()
		}, byPath, 1)) > 0
		return nil
	})
	return ok, err
}

func (st stampStore) PruneTombstones(ctx context.Context, before outbox.HLC) (int64, error) {
	var n int64
	err := st.s.Do(ctx, func() error {
		n = st.t().DeleteWhere(func(_ stampKey, v model.ItemStamp) bool {
			return v.Deleted && v.Time.Compare(before) < 0
		})
		return nil
	})
	return n, err
}

func (st stampStore) Clear(ctx context.Context, repoKey string) error {
	return st.s.Do(ctx, func() error {
		st.t().DeleteWhere(func(k stampKey, _ model.ItemStamp) bool { return k.repo == repoKey })
		return nil
	})
}
