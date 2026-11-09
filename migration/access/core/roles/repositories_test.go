package roles_test

import (
	"context"

	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// repositories stands in for the artifacts module's API with the two calls
// roles makes, List and Update, over the test store's repository store. Like
// the module's binding, it passes the context on, so its calls join the
// transaction the service runs them in. When fail points at "Repos.List" or
// "Repos.Update", that call fails with errBoom instead. Any other method is
// the embedded nil interface's and panics, so a call roles must not make
// shows at once.
type repositories struct {
	artifactsapi.Repositories
	db   *inmem.Store
	fail *string
}

func (r repositories) failing(name string) bool { return r.fail != nil && *r.fail == name }

func (r repositories) List(ctx context.Context, f artifactsapi.RepoFilter) ([]repo.Config, error) {
	if r.failing("Repos.List") {
		return nil, errBoom
	}
	return r.db.Repos().List(ctx, store.RepoFilter(f)) //nolint:wrapcheck // test double
}

func (r repositories) Update(ctx context.Context, c repo.Config) error {
	if r.failing("Repos.Update") {
		return errBoom
	}
	return r.db.Repos().Update(ctx, c) //nolint:wrapcheck // test double
}
