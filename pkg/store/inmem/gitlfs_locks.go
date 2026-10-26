package inmem

import (
	"cmp"
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// GitLFSLocks implements store.Store.
func (s *Store) GitLFSLocks() store.GitLFSLockStore { return gitLFSLocks{s} }

type gitLFSLocks struct{ s *Store }

func (l gitLFSLocks) rows() *table[string, types.GitLFSLock] {
	return tableOf[string, types.GitLFSLock](l.s, "gitlfs_locks")
}

func (l gitLFSLocks) Create(ctx context.Context, v types.GitLFSLock) error {
	if v.ID == "" || v.RepoKey == "" || v.Path == "" || v.Owner == "" || v.LockedAt.IsZero() {
		return fmt.Errorf("create Git LFS lock: %w", store.ErrInvalid)
	}
	return l.s.do(ctx, func() error {
		if !l.s.Repos().(repos).t().has(v.RepoKey) {
			return notFound("repository", v.RepoKey)
		}
		if l.rows().has(v.ID) {
			return conflict("Git LFS lock", v.ID)
		}
		for _, row := range l.rows().rows {
			if row.RepoKey == v.RepoKey && row.Path == v.Path {
				return conflict("Git LFS lock path", v.Path)
			}
		}
		l.rows().put(v.ID, store.Normalise(v))
		return nil
	})
}

func (l gitLFSLocks) Get(ctx context.Context, repoKey, id string) (types.GitLFSLock, error) {
	var out types.GitLFSLock
	err := l.s.do(ctx, func() error {
		var ok bool
		out, ok = l.rows().get(id)
		if !ok || out.RepoKey != repoKey {
			return notFound("Git LFS lock", id)
		}
		return nil
	})
	return out, err
}

func (l gitLFSLocks) List(
	ctx context.Context, f store.GitLFSLockFilter, p store.Page,
) ([]types.GitLFSLock, string, error) {
	if f.RepoKey == "" {
		return nil, "", fmt.Errorf("list Git LFS locks: %w", store.ErrInvalid)
	}
	var out []types.GitLFSLock
	var next string
	err := l.s.do(ctx, func() error {
		rows := l.rows().all(func(_ string, v types.GitLFSLock) bool {
			return v.RepoKey == f.RepoKey && (f.Path == "" || v.Path == f.Path) &&
				(f.ID == "" || v.ID == f.ID)
		}, func(a, b types.GitLFSLock) int { return cmp.Compare(a.Path, b.Path) })
		var err error
		out, next, err = paginate(rows, p, false, func(v types.GitLFSLock) []any {
			return []any{v.Path}
		})
		return err
	})
	return out, next, err
}

func (l gitLFSLocks) Delete(ctx context.Context, repoKey, id string) (types.GitLFSLock, error) {
	var out types.GitLFSLock
	err := l.s.do(ctx, func() error {
		var ok bool
		out, ok = l.rows().get(id)
		if !ok || out.RepoKey != repoKey {
			return notFound("Git LFS lock", id)
		}
		l.rows().del(id)
		return nil
	})
	return out, err
}
