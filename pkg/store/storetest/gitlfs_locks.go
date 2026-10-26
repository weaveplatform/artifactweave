package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func runGitLFSLocks(t *testing.T, newStore Factory) {
	t.Run("GitLFSLocks", func(t *testing.T) {
		t.Run("ExclusivePath", func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()
			gitLFSRepo(t, s, "lfs-local")
			lock := gitLFSLock("lock-1", "lfs-local", "src/a.bin")
			must(t, s.GitLFSLocks().Create(ctx, lock))
			wantErr(t, s.GitLFSLocks().Create(ctx, gitLFSLock("lock-2", "lfs-local", lock.Path)), store.ErrConflict)
			wantErr(t, s.GitLFSLocks().Create(ctx, gitLFSLock(lock.ID, "lfs-local", "src/b.bin")), store.ErrConflict)
			same(t, "original lock after collisions", lock,
				get[types.GitLFSLock](t)(s.GitLFSLocks().Get(ctx, lock.RepoKey, lock.ID)))
			invalid := lock
			invalid.ID = ""
			wantErr(t, s.GitLFSLocks().Create(ctx, invalid), store.ErrInvalid)
			wantErr(t, s.GitLFSLocks().Create(ctx, gitLFSLock("other", "absent", "a.bin")), store.ErrNotFound)
		})
		t.Run("ConcurrentExclusivePath", func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()
			gitLFSRepo(t, s, "lfs-local")
			const contenders = 8
			results := make(chan error, contenders)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range contenders {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- s.GitLFSLocks().Create(ctx,
						gitLFSLock(fmt.Sprintf("lock-%d", i), "lfs-local", "same.bin"))
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			created := 0
			for err := range results {
				switch {
				case err == nil:
					created++
				case errors.Is(err, store.ErrConflict):
				default:
					t.Errorf("competing create: %v, want conflict", err)
				}
			}
			if created != 1 {
				t.Fatalf("created %d locks for one path, want 1", created)
			}
		})
		t.Run("OrderedPagesAndFilters", func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()
			gitLFSRepo(t, s, "lfs-local")
			ordered := []types.GitLFSLock{
				gitLFSLock("a", "lfs-local", "a.bin"),
				gitLFSLock("b", "lfs-local", "b.bin"),
				gitLFSLock("c", "lfs-local", "c.bin"),
			}
			for i := len(ordered) - 1; i >= 0; i-- {
				must(t, s.GitLFSLocks().Create(ctx, ordered[i]))
			}
			f := store.GitLFSLockFilter{RepoKey: "lfs-local"}
			list := func(p store.Page) ([]types.GitLFSLock, string, error) {
				return s.GitLFSLocks().List(ctx, f, p)
			}
			same(t, "ordered paginated locks", ordered, pages(t, 1, list))
			for _, filter := range []store.GitLFSLockFilter{
				{RepoKey: "lfs-local", Path: "b.bin"},
				{RepoKey: "lfs-local", ID: "b"},
			} {
				rows, next, err := s.GitLFSLocks().List(ctx, filter, store.Page{})
				must(t, err)
				same(t, "filtered lock", []types.GitLFSLock{ordered[1]}, rows)
				if next != "" {
					t.Fatalf("filtered list next = %q", next)
				}
			}
			rows, _, err := s.GitLFSLocks().List(ctx,
				store.GitLFSLockFilter{RepoKey: "lfs-local", Path: "missing"}, store.Page{})
			must(t, err)
			if len(rows) != 0 {
				t.Fatalf("missing path returned %d locks", len(rows))
			}
			_, _, err = s.GitLFSLocks().List(ctx, store.GitLFSLockFilter{}, store.Page{})
			wantErr(t, err, store.ErrInvalid)
			for _, cursor := range []string{"%%%", store.Cursor("a", "b")} {
				_, _, err = list(store.Page{After: cursor})
				wantErr(t, err, store.ErrInvalidCursor)
			}
		})
		t.Run("TransactionRollback", func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()
			gitLFSRepo(t, s, "lfs-local")
			lock := gitLFSLock("lock-1", "lfs-local", "a.bin")
			rollback := errors.New("rollback")
			err := s.Tx(ctx, func(txCtx context.Context) error {
				if err := s.GitLFSLocks().Create(txCtx, lock); err != nil {
					return err
				}
				return rollback
			})
			wantErr(t, err, rollback)
			_, err = s.GitLFSLocks().Get(ctx, lock.RepoKey, lock.ID)
			wantErr(t, err, store.ErrNotFound)
			must(t, s.GitLFSLocks().Create(ctx, lock))
			err = s.Tx(ctx, func(txCtx context.Context) error {
				_, err := s.GitLFSLocks().Delete(txCtx, lock.RepoKey, lock.ID)
				if err != nil {
					return err
				}
				return rollback
			})
			wantErr(t, err, rollback)
			same(t, "lock survives rolled back delete", lock,
				get[types.GitLFSLock](t)(s.GitLFSLocks().Get(ctx, lock.RepoKey, lock.ID)))
		})
		t.Run("RepositoryScopeAndDelete", func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()
			gitLFSRepo(t, s, "lfs-one")
			gitLFSRepo(t, s, "lfs-two")
			one := gitLFSLock("one", "lfs-one", "same.bin")
			two := gitLFSLock("two", "lfs-two", "same.bin")
			must(t, s.GitLFSLocks().Create(ctx, one))
			must(t, s.GitLFSLocks().Create(ctx, two))
			_, err := s.GitLFSLocks().Get(ctx, "lfs-two", one.ID)
			wantErr(t, err, store.ErrNotFound)
			_, err = s.GitLFSLocks().Delete(ctx, "lfs-two", one.ID)
			wantErr(t, err, store.ErrNotFound)
			must(t, s.Repos().Delete(ctx, "lfs-one"))
			_, err = s.GitLFSLocks().Get(ctx, "lfs-one", one.ID)
			wantErr(t, err, store.ErrNotFound)
			same(t, "other repository lock remains", two,
				get[types.GitLFSLock](t)(s.GitLFSLocks().Get(ctx, "lfs-two", two.ID)))
			deleted := get[types.GitLFSLock](t)(s.GitLFSLocks().Delete(ctx, "lfs-two", two.ID))
			same(t, "deleted lock", two, deleted)
			_, err = s.GitLFSLocks().Get(ctx, "lfs-two", two.ID)
			wantErr(t, err, store.ErrNotFound)
		})
	})
}

func gitLFSRepo(t *testing.T, s store.Store, key string) {
	t.Helper()
	must(t, s.Repos().Create(t.Context(), repo.Config{
		Key: key, Class: repo.Local, PackageType: repo.GitLFS,
	}))
}

func gitLFSLock(id, repoKey, path string) types.GitLFSLock {
	return types.GitLFSLock{ID: id, RepoKey: repoKey, Path: path, Owner: "alice", LockedAt: at(1)}
}
