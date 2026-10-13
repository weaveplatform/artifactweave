package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/repo"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
)

// Repos implements store.Store.
func (s *Store) Repos() store.RepoStore { return repos{s} }

// Items implements store.Store.
func (s *Store) Items() store.ItemStore { return items{s} }

// Uploads implements store.Store.
func (s *Store) Uploads() store.UploadStore { return uploads{s} }

// Trash implements store.Store.
func (s *Store) Trash() store.TrashStore { return trash{s} }

// Packages implements store.Store.
func (s *Store) Packages() store.PackageStore { return packages{s} }

type repos struct{ s *Store }

func (r repos) t() *table[string, repo.Config] {
	return tableOf[string, repo.Config](r.s, "repositories")
}

func (r repos) Create(ctx context.Context, c repo.Config) error {
	if c.Key == "" {
		return fmt.Errorf("create repository: key is required: %w", store.ErrInvalid)
	}
	return r.s.do(ctx, func() error {
		if r.t().has(c.Key) {
			return conflict("repository", c.Key)
		}
		r.t().put(c.Key, c)
		return nil
	})
}

func (r repos) Get(ctx context.Context, key string) (repo.Config, error) {
	var out repo.Config
	err := r.s.do(ctx, func() error {
		var ok bool
		if out, ok = r.t().get(key); !ok {
			return notFound("repository", key)
		}
		return nil
	})
	return out, err
}

func (r repos) Update(ctx context.Context, c repo.Config) error {
	return r.s.do(ctx, func() error {
		if !r.t().has(c.Key) {
			return notFound("repository", c.Key)
		}
		r.t().put(c.Key, c)
		return nil
	})
}

func (r repos) Delete(ctx context.Context, key string) error {
	return r.s.do(ctx, func() error {
		if !r.t().del(key) {
			return notFound("repository", key)
		}
		return nil
	})
}

func compareRepos(a, b repo.Config) int { return cmp.Compare(a.Key, b.Key) }

func (r repos) List(ctx context.Context, f store.RepoFilter) ([]repo.Config, error) {
	var out []repo.Config
	err := r.s.do(ctx, func() error {
		out = r.t().all(func(_ string, c repo.Config) bool {
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
	err := r.s.do(ctx, func() error {
		for _, c := range r.t().all(func(_ string, c repo.Config) bool { return keep(c) }, compareRepos) {
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
