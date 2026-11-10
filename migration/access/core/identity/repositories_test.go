package identity_test

import (
	"context"

	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// repositories stands in for the artifacts module's API with the one call
// the seed makes, Create, over the test store's repository store. Like the
// module's binding, it passes the context on, so the build-info repository
// is created in the seed's transaction. Any other method is the embedded nil
// interface's and panics.
type repositories struct {
	artifactsapi.Repositories
	db *inmem.Store
}

func (r repositories) Create(ctx context.Context, c repo.Config) error {
	return r.db.Repos().Create(ctx, c) //nolint:wrapcheck // test double
}
