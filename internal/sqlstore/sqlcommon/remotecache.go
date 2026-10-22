package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RemoteCache implements store.Store.
func (s *Store) RemoteCache() store.RemoteCacheStore {
	return store.NewRemoteCache(s, remoteRecords{s})
}

type remoteRecords struct{ s *Store }

func (r remoteRecords) Source(ctx context.Context, key string) (types.RemoteSourceState, error) {
	var doc sql.NullString
	// The lifetime lock precedes the cache row lock in every mutation, including
	// configuration edits and deletion. No network I/O runs while it is held.
	var found string
	err := r.s.q(ctx).
		QueryRowContext(ctx, r.s.d.Rebind("SELECT key FROM repositories WHERE key = ?"+r.s.lockClause(ctx)), key).
		Scan(&found)
	if err != nil {
		return types.RemoteSourceState{}, r.s.wrap("lock remote repository", err)
	}
	err = r.s.q(ctx).
		QueryRowContext(ctx, r.s.d.Rebind("SELECT doc FROM remote_sources WHERE repo_key = ?"+r.s.lockClause(ctx)), key).
		Scan(&doc)
	var out types.RemoteSourceState
	if err != nil {
		return out, r.s.wrap("read remote source", err)
	}
	err = fromJSON(doc, &out)
	return out, err
}

func (r remoteRecords) PutSource(ctx context.Context, key string, s types.RemoteSourceState) error {
	_, err := r.s.exec(
		ctx,
		"write remote source",
		Upsert("remote_sources", []string{"repo_key", "doc"}, []string{"repo_key"}),
		key,
		mustJSON(store.Normalise(s)),
	)
	return err
}

func (r remoteRecords) Entry(
	ctx context.Context,
	k types.RemoteCacheKey,
) (types.RemoteCacheEntry, error) {
	var doc sql.NullString
	err := r.s.q(ctx).
		QueryRowContext(ctx, r.s.d.Rebind("SELECT doc FROM remote_cache WHERE repo_key = ? AND cache_id = ?"), k.RepoKey, store.RemoteCacheID(k)).
		Scan(&doc)
	var out types.RemoteCacheEntry
	if err != nil {
		return out, r.s.wrap("read remote cache", err)
	}
	if err = fromJSON(doc, &out); err != nil {
		return out, err
	}
	if out.Token.Key != k {
		return out, store.ErrConflict
	}
	return out, nil
}

func (r remoteRecords) PutEntry(ctx context.Context, e types.RemoteCacheEntry) error {
	_, err := r.s.exec(
		ctx,
		"write remote cache",
		Upsert(
			"remote_cache",
			[]string{"repo_key", "cache_id", "path", "doc"},
			[]string{"repo_key", "cache_id"},
		),
		e.Token.Key.RepoKey,
		store.RemoteCacheID(e.Token.Key),
		e.Token.Key.Path,
		mustJSON(store.Normalise(e)),
	)
	return err
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
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("remote cache page: %w", err)
	}
	after, err := p.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("remote cache cursor: %w", err)
	}
	q := "SELECT doc FROM remote_cache WHERE repo_key = ?"
	args := []any{key}
	if exact {
		q += " AND path = ?"
		args = append(args, prefix)
	} else if prefix != "" {
		// LIKE is ASCII case-insensitive on SQLite. Repository paths are
		// case-sensitive and wildcard characters are literal on both stores.
		q += " AND (path = ? OR substr(path, 1, length(?)) = ?)"
		args = append(args, prefix, prefix+"/", prefix+"/")
	}
	if after != nil {
		q += " AND cache_id > ?"
		args = append(args, after[0])
	}
	q += " ORDER BY cache_id LIMIT ?"
	args = append(args, p.Limit+1)
	var out []types.RemoteCacheEntry
	err = r.s.rows(ctx, "list remote cache", q, args, func(rows *sql.Rows) error {
		var doc sql.NullString
		if err := rows.Scan(&doc); err != nil {
			return err //nolint:wrapcheck // rows wraps scan errors
		}
		var e types.RemoteCacheEntry
		if err := fromJSON(doc, &e); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := store.Trim(
		out,
		p.Limit,
		func(e types.RemoteCacheEntry) []string { return []string{store.RemoteCacheID(e.Token.Key)} },
	)
	return out, next, nil
}
