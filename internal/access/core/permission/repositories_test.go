package permission_test

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// repositories stands in for the artifacts module's API with the one call
// permission makes, Get, over the test store's repository store. When fail
// points at "repos.get", Get fails with errBoom instead. Like the module's
// binding, it returns the store's errors unwrapped. Any other method is the
// embedded nil interface's and panics, so a call permission must not make
// shows at once.
type repositories struct {
	artifactsapi.Repositories
	db   *inmem.Store
	fail *string
}

func (r repositories) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.fail != nil && *r.fail == "repos.get" {
		return repo.Config{}, errBoom
	}
	return r.db.Repos().Get(ctx, key) //nolint:wrapcheck // test double
}
