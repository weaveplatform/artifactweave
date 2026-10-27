package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// GitLFSLocks implements store.Store.
func (s *Store) GitLFSLocks() store.GitLFSLockStore { return gitLFSLocks{s} }

type gitLFSLocks struct{ s *Store }

const gitLFSLockCols = "id, repo_key, path, owner, locked_at"

func scanGitLFSLock(row interface{ Scan(...any) error }) (types.GitLFSLock, error) {
	var out types.GitLFSLock
	var stamp int64
	if err := row.Scan(&out.ID, &out.RepoKey, &out.Path, &out.Owner, &stamp); err != nil {
		return out, err //nolint:wrapcheck // caller names operation
	}
	out.LockedAt = time.UnixMicro(stamp).UTC()
	return out, nil
}

func (l gitLFSLocks) Create(ctx context.Context, v types.GitLFSLock) error {
	if v.ID == "" || v.RepoKey == "" || v.Path == "" || v.Owner == "" || v.LockedAt.IsZero() {
		return fmt.Errorf("create git lfs lock: %w", store.ErrInvalid)
	}
	if _, err := l.s.Repos().Get(ctx, v.RepoKey); err != nil {
		return fmt.Errorf("create git lfs lock %s: %w", v.ID, err)
	}
	_, err := l.s.exec(ctx, "create Git LFS lock "+v.ID,
		Insert("gitlfs_locks", strings.Split(gitLFSLockCols, ", ")),
		v.ID, v.RepoKey, v.Path, v.Owner, store.Normalise(v.LockedAt).UnixMicro())
	return err
}

func (l gitLFSLocks) Get(ctx context.Context, repoKey, id string) (types.GitLFSLock, error) {
	out, err := scanGitLFSLock(l.s.q(ctx).QueryRowContext(ctx,
		l.s.d.Rebind("SELECT "+gitLFSLockCols+" FROM gitlfs_locks WHERE repo_key = ? AND id = ?"),
		repoKey, id))
	return out, l.s.wrap("get Git LFS lock "+id, err)
}

func (l gitLFSLocks) List(
	ctx context.Context, f store.GitLFSLockFilter, p store.Page,
) ([]types.GitLFSLock, string, error) {
	if f.RepoKey == "" {
		return nil, "", fmt.Errorf("list git lfs locks: %w", store.ErrInvalid)
	}
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("list git lfs locks: %w", err)
	}
	cursor, err := p.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("list git lfs locks: %w", err)
	}
	where := " WHERE repo_key = ?"
	args := []any{f.RepoKey}
	if f.Path != "" {
		where += " AND path = ?"
		args = append(args, f.Path)
	}
	if f.ID != "" {
		where += " AND id = ?"
		args = append(args, f.ID)
	}
	if cursor != nil {
		where += " AND path > ?"
		args = append(args, cursor[0])
	}
	var out []types.GitLFSLock
	err = l.s.rows(ctx, "list Git LFS locks",
		"SELECT "+gitLFSLockCols+" FROM gitlfs_locks"+where+" ORDER BY path LIMIT ?",
		append(args, p.Limit+1), func(r *sql.Rows) error {
			v, err := scanGitLFSLock(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	out, next := store.Trim(out, p.Limit, func(v types.GitLFSLock) []string {
		return []string{v.Path}
	})
	return out, next, nil
}

func (l gitLFSLocks) Delete(ctx context.Context, repoKey, id string) (types.GitLFSLock, error) {
	var out types.GitLFSLock
	err := l.s.Tx(ctx, func(ctx context.Context) error {
		var err error
		out, err = l.Get(ctx, repoKey, id)
		if err != nil {
			return err
		}
		return l.s.execOne(ctx, "delete Git LFS lock "+id,
			"DELETE FROM gitlfs_locks WHERE repo_key = ? AND id = ?", repoKey, id)
	})
	return out, err
}
