package repos

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// NewProjectRepositoryReader binds the narrow reader before module assembly,
// so access authorization need not depend on the artifacts module constructor.
func NewProjectRepositoryReader(store interface{ Repos() port.RepoStore }) api.ProjectRepositoryReader {
	return projectRepositoryReader{store: store}
}

type projectRepositoryReader struct {
	store interface{ Repos() port.RepoStore }
}

func (r projectRepositoryReader) ProjectRepositories(ctx context.Context, key string, page kernel.Page) ([]api.ProjectRepository, string, error) {
	return r.store.Repos().ProjectRepositories(ctx, key, page) //nolint:wrapcheck // the repository port supplies its context
}
