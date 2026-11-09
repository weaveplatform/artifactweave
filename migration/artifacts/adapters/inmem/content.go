package inmem

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Repos implements store.Store.
func (s *Store) Repos() store.RepoStore { return repos{s} }

type repos struct{ s *Store }

func (r repos) t() *memtx.Table[string, repo.Config] {
	return memtx.TableOf[string, repo.Config](r.s.Store, "repositories")
}

func (r repos) Create(ctx context.Context, c repo.Config) error {
	if c.Key == "" {
		return fmt.Errorf("create repository: key is required: %w", store.ErrInvalid)
	}
	return r.s.Tx(ctx, func(ctx context.Context) error {
		if r.t().Has(c.Key) {
			return memtx.Conflict("repository", c.Key)
		}
		r.t().Put(c.Key, c)
		return (remoteRecords{r.s}).PutSource(
			ctx,
			c.Key,
			types.RemoteSourceState{Source: store.NewRemoteSource(c.Key)},
		)
	})
}

func (r repos) Get(ctx context.Context, key string) (repo.Config, error) {
	var out repo.Config
	err := r.s.Do(ctx, func() error {
		var ok bool
		if out, ok = r.t().Get(key); !ok {
			return memtx.NotFound("repository", key)
		}
		return nil
	})
	return out, err
}

func (r repos) Update(ctx context.Context, c repo.Config) error {
	return r.s.Tx(ctx, func(ctx context.Context) error {
		if !r.t().Has(c.Key) {
			return memtx.NotFound("repository", c.Key)
		}
		old, _ := r.t().Get(c.Key)
		if store.RemotePolicyChanged(old, c) {
			state, err := (remoteRecords{r.s}).Source(ctx, c.Key)
			if err != nil {
				return err
			}
			if store.RemoteConfigChanged(old, c) {
				state.Source.Generation = types.SourceGeneration(rand.Text())
				state.ConnectionFailures = 0
				state.OfflineUntil = time.Time{}
				state.RetryAfterUntil = time.Time{}
			}
			state.PolicyRevision++
			state.Revision++
			if err := (remoteRecords{r.s}).PutSource(ctx, c.Key, state); err != nil {
				return err
			}
		}
		r.t().Put(c.Key, c)
		return nil
	})
}

func (r repos) Delete(ctx context.Context, key string) error {
	return r.s.Tx(ctx, func(ctx context.Context) error {
		if !r.t().Has(key) {
			return memtx.NotFound("repository", key)
		}
		if _, err := r.s.RemoteCache().Invalidate(ctx, key, "", false); err != nil {
			return err
		}
		r.t().Del(key)
		(gitLFSLocks{r.s}).rows().DeleteWhere(func(_ string, v types.GitLFSLock) bool {
			return v.RepoKey == key
		})
		(bowerRegistrations{r.s}).rows().
			DeleteWhere(func(_ string, v types.BowerRegistration) bool {
				return v.RepoKey == key
			})
		(remoteRecords{r.s}).sources().Del(key)
		(remoteRecords{r.s}).entries().
			DeleteWhere(func(_ string, e types.RemoteCacheEntry) bool { return e.Token.Key.RepoKey == key })
		return nil
	})
}

func compareRepos(a, b repo.Config) int { return cmp.Compare(a.Key, b.Key) }

func (r repos) List(ctx context.Context, f store.RepoFilter) ([]repo.Config, error) {
	var out []repo.Config
	err := r.s.Do(ctx, func() error {
		out = r.t().All(func(_ string, c repo.Config) bool {
			return (f.Class == "" || c.Class == f.Class) &&
				(f.PackageType == "" || c.PackageType == f.PackageType) &&
				(f.ProjectKey == "" || c.ProjectKey == f.ProjectKey)
		}, compareRepos)
		return nil
	})
	return out, err
}

// keysWhere returns the sorted keys of repositories matching keep.
func (r repos) keysWhere(ctx context.Context, keep func(repo.Config) bool) ([]string, error) {
	var out []string
	err := r.s.Do(ctx, func() error {
		for _, c := range r.t().All(func(_ string, c repo.Config) bool { return keep(c) }, compareRepos) {
			out = append(out, c.Key)
		}
		return nil
	})
	return out, err
}

func (r repos) Including(ctx context.Context, key string) ([]string, error) {
	return r.keysWhere(ctx, func(c repo.Config) bool {
		return c.Class == repo.Virtual && slices.Contains(c.Repositories, key)
	})
}

func (r repos) UsingKeypair(ctx context.Context, name string) ([]string, error) {
	return r.keysWhere(ctx, func(c repo.Config) bool {
		return name != "" && (c.PrimaryKeyPairRef == name || c.SecondaryKeyPairRef == name)
	})
}
