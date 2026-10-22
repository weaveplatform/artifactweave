package inmem

import (
	"cmp"
	"context"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RemoteCache implements store.Store.
func (s *Store) RemoteCache() store.RemoteCacheStore {
	return store.NewRemoteCache(s, remoteRecords{s})
}

type remoteRecords struct{ s *Store }

func (r remoteRecords) sources() *table[string, types.RemoteSourceState] {
	return tableOf[string, types.RemoteSourceState](r.s, "remote_sources")
}

func (r remoteRecords) entries() *table[string, types.RemoteCacheEntry] {
	return tableOf[string, types.RemoteCacheEntry](r.s, "remote_cache")
}

func (r remoteRecords) Source(ctx context.Context, key string) (types.RemoteSourceState, error) {
	var out types.RemoteSourceState
	err := r.s.do(ctx, func() error {
		var ok bool
		out, ok = r.sources().get(key)
		if !ok {
			return notFound("remote source", key)
		}
		return nil
	})
	return out, err
}

func (r remoteRecords) PutSource(ctx context.Context, key string, s types.RemoteSourceState) error {
	return r.s.do(ctx, func() error { r.sources().put(key, s); return nil })
}

func (r remoteRecords) Entry(
	ctx context.Context,
	k types.RemoteCacheKey,
) (types.RemoteCacheEntry, error) {
	var out types.RemoteCacheEntry
	err := r.s.do(ctx, func() error {
		var ok bool
		out, ok = r.entries().get(store.RemoteCacheID(k))
		if !ok {
			return notFound("remote cache", k.Path)
		}
		if out.Token.Key != k {
			return store.ErrConflict
		}
		return nil
	})
	return out, err
}

func (r remoteRecords) PutEntry(ctx context.Context, e types.RemoteCacheEntry) error {
	return r.s.do(
		ctx,
		func() error { r.entries().put(store.RemoteCacheID(e.Token.Key), e); return nil },
	)
}

func (r remoteRecords) Entries(
	ctx context.Context,
	key, prefix string,
	p store.Page,
) ([]types.RemoteCacheEntry, string, error) {
	return r.listEntries(ctx, key, prefix, p, false)
}

func (r remoteRecords) Exact(
	ctx context.Context,
	key, path string,
	p store.Page,
) ([]types.RemoteCacheEntry, string, error) {
	return r.listEntries(ctx, key, path, p, true)
}

func (r remoteRecords) listEntries(
	ctx context.Context,
	key, prefix string,
	p store.Page,
	exact bool,
) ([]types.RemoteCacheEntry, string, error) {
	var out []types.RemoteCacheEntry
	var next string
	err := r.s.do(ctx, func() error {
		rows := r.entries().all(func(_ string, e types.RemoteCacheEntry) bool {
			return e.Token.Key.RepoKey == key &&
				(exact && e.Token.Key.Path == prefix || !exact && store.RemotePathMatches(e.Token.Key.Path, prefix))
		},
			func(a, b types.RemoteCacheEntry) int {
				return cmp.Compare(
					store.RemoteCacheID(a.Token.Key),
					store.RemoteCacheID(b.Token.Key),
				)
			})
		var err error
		out, next, err = paginate(
			rows,
			p,
			false,
			func(e types.RemoteCacheEntry) []any { return []any{store.RemoteCacheID(e.Token.Key)} },
		)
		return err
	})
	return out, next, err
}
