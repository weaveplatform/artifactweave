package docker_test

import (
	"context"
	"errors"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

var errBoom = errors.New("boom")

type faultyStore struct {
	*inmem.Store
	fail map[string]bool
}

func (s faultyStore) Repos() port.RepoStore {
	return faultyRepos{RepoStore: s.Store.Repos(), fail: s.fail}
}

type faultyRepos struct {
	port.RepoStore
	fail map[string]bool
}

func (r faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.fail["get"] || r.fail["get:"+key] {
		return repo.Config{}, errBoom
	}
	return r.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}
