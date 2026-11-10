package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// RemoteCache returns the remote cache store: storekit's compare-and-swap
// logic over the remote_sources and remote_cache rows (decision 0007).
func (s *Store) RemoteCache() port.RemoteCacheStore {
	return storekit.NewRemoteCache(s, remoteRecords{s})
}

type remoteRecords struct{ s *Store }

func (r remoteRecords) Source(ctx context.Context, key string) (model.RemoteSourceState, error) {
	var doc sql.NullString
	// The lifetime lock precedes the cache row lock in every mutation, including
	// configuration edits and deletion. No network I/O runs while it is held.
	var found string
	err := r.s.Q(ctx).
		QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT key FROM repositories WHERE key = ?"+r.s.LockClause(ctx)), key).
		Scan(&found)
	if err != nil {
		return model.RemoteSourceState{}, r.s.Wrap("lock remote repository", err)
	}
	err = r.s.Q(ctx).
		QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT doc FROM remote_sources WHERE repo_key = ?"+r.s.LockClause(ctx)), key).
		Scan(&doc)
	var out model.RemoteSourceState
	if err != nil {
		return out, r.s.Wrap("read remote source", err)
	}
	err = dialect.FromJSON(doc, &out)
	return out, err
}

func (r remoteRecords) PutSource(ctx context.Context, key string, s model.RemoteSourceState) error {
	_, err := r.s.Exec(
		ctx,
		"write remote source",
		dialect.Upsert("remote_sources", []string{"repo_key", "doc"}, []string{"repo_key"}),
		key,
		dialect.MustJSON(kernel.Normalise(s)),
	)
	return err
}

func (r remoteRecords) Entry(
	ctx context.Context,
	k model.RemoteCacheKey,
) (model.RemoteCacheEntry, error) {
	var doc sql.NullString
	err := r.s.Q(ctx).
		QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT doc FROM remote_cache WHERE repo_key = ? AND cache_id = ?"), k.RepoKey, port.RemoteCacheID(k)).
		Scan(&doc)
	var out model.RemoteCacheEntry
	if err != nil {
		return out, r.s.Wrap("read remote cache", err)
	}
	if err = dialect.FromJSON(doc, &out); err != nil {
		return out, err
	}
	if out.Token.Key != k {
		return out, kernel.ErrConflict
	}
	return out, nil
}

func (r remoteRecords) PutEntry(ctx context.Context, e model.RemoteCacheEntry) error {
	_, err := r.s.Exec(
		ctx,
		"write remote cache",
		dialect.Upsert(
			"remote_cache",
			[]string{"repo_key", "cache_id", "path", "doc"},
			[]string{"repo_key", "cache_id"},
		),
		e.Token.Key.RepoKey,
		port.RemoteCacheID(e.Token.Key),
		e.Token.Key.Path,
		dialect.MustJSON(kernel.Normalise(e)),
	)
	return err
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
	var out []model.RemoteCacheEntry
	err = r.s.Rows(ctx, "list remote cache", q, args, func(rows *sql.Rows) error {
		var doc sql.NullString
		if err := rows.Scan(&doc); err != nil {
			return err //nolint:wrapcheck // Rows wraps scan errors
		}
		var e model.RemoteCacheEntry
		if err := dialect.FromJSON(doc, &e); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(
		out,
		p.Limit,
		func(e model.RemoteCacheEntry) []string { return []string{port.RemoteCacheID(e.Token.Key)} },
	)
	return out, next, nil
}
