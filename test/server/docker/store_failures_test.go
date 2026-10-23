package docker_test

import (
	"context"
	"errors"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

var errBoom = errors.New("boom")

type faultyStore struct {
	*inmem.Store
	fail map[string]bool
}

func (s faultyStore) Repos() store.RepoStore {
	return faultyRepos{RepoStore: s.Store.Repos(), fail: s.fail}
}

type faultyRepos struct {
	store.RepoStore
	fail map[string]bool
}

func (r faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.fail["get"] || r.fail["get:"+key] {
		return repo.Config{}, errBoom
	}
	return r.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}
