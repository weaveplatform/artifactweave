package permission_test

import (
	"context"

	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// repositories stands in for the artifacts module's API with the one call
// permission makes, Get, over a test store's repository store, so a store
// that fails its repository reads fails them here too. Like the module's
// binding, it returns the store's errors unwrapped. Any other method is the
// embedded nil interface's and panics, so a call permission must not make
// shows at once.
type repositories struct {
	artifactsapi.Repositories
	db interface{ Repos() store.RepoStore }
}

func (r repositories) Get(ctx context.Context, key string) (repo.Config, error) {
	return r.db.Repos().Get(ctx, key) //nolint:wrapcheck // test double
}
