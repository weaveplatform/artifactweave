package inmem

import (
	"cmp"
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// RemoteCache returns the remote cache store: storekit's compare-and-swap
// logic over the remote_sources and remote_cache rows (decision 0007).
func (s *Store) RemoteCache() port.RemoteCacheStore {
	return storekit.NewRemoteCache(s, remoteRecords{s})
}

type remoteRecords struct{ s *Store }

func (r remoteRecords) sources() *memtx.Table[string, model.RemoteSourceState] {
	return memtx.TableOf[string, model.RemoteSourceState](r.s.Store, "remote_sources")
}

func (r remoteRecords) entries() *memtx.Table[string, model.RemoteCacheEntry] {
	return memtx.TableOf[string, model.RemoteCacheEntry](r.s.Store, "remote_cache")
}

func (r remoteRecords) Source(ctx context.Context, key string) (model.RemoteSourceState, error) {
	var out model.RemoteSourceState
	err := r.s.Do(ctx, func() error {
		var ok bool
		out, ok = r.sources().Get(key)
		if !ok {
			return memtx.NotFound("remote source", key)
		}
		return nil
	})
	return out, err
}

func (r remoteRecords) PutSource(ctx context.Context, key string, s model.RemoteSourceState) error {
	return r.s.Do(ctx, func() error { r.sources().Put(key, s); return nil })
}

func (r remoteRecords) Entry(
	ctx context.Context,
	k model.RemoteCacheKey,
) (model.RemoteCacheEntry, error) {
	var out model.RemoteCacheEntry
	err := r.s.Do(ctx, func() error {
		var ok bool
		out, ok = r.entries().Get(port.RemoteCacheID(k))
		if !ok {
			return memtx.NotFound("remote cache", k.Path)
		}
		if out.Token.Key != k {
			return kernel.ErrConflict
		}
		return nil
	})
	return out, err
}

func (r remoteRecords) PutEntry(ctx context.Context, e model.RemoteCacheEntry) error {
	return r.s.Do(
		ctx,
		func() error { r.entries().Put(port.RemoteCacheID(e.Token.Key), e); return nil },
	)
}

func (r remoteRecords) Entries(
	ctx context.Context,
	key, prefix string,
	p kernel.Page,
) ([]model.RemoteCacheEntry, string, error) {
	return r.listEntries(ctx, key, prefix, p, false)
}

func (r remoteRecords) Exact(
	ctx context.Context,
	key, path string,
	p kernel.Page,
) ([]model.RemoteCacheEntry, string, error) {
	return r.listEntries(ctx, key, path, p, true)
}

func (r remoteRecords) listEntries(
	ctx context.Context,
	key, prefix string,
	p kernel.Page,
	exact bool,
) ([]model.RemoteCacheEntry, string, error) {
	var out []model.RemoteCacheEntry
	var next string
	err := r.s.Do(ctx, func() error {
		rows := r.entries().All(func(_ string, e model.RemoteCacheEntry) bool {
			return e.Token.Key.RepoKey == key &&
				(exact && e.Token.Key.Path == prefix || !exact && port.RemotePathMatches(e.Token.Key.Path, prefix))
		},
			func(a, b model.RemoteCacheEntry) int {
				return cmp.Compare(
					port.RemoteCacheID(a.Token.Key),
					port.RemoteCacheID(b.Token.Key),
				)
			})
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(e model.RemoteCacheEntry) []any { return []any{port.RemoteCacheID(e.Token.Key)} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}
