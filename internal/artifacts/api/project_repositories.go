package api

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ProjectRepository is a descriptive ownership/environment snapshot. It
// contains no upstream URL, credentials, headers or authorization decision.
type ProjectRepository struct {
	Key          string
	Class        repo.Class
	ProjectKey   string
	Environments []string
}

// ProjectRepositoryReader supplies current project-owned resources without
// hydrating configuration secrets. Sharing is a separate contract. A caller
// requiring a coherent authorization/publication snapshot holds its enclosing
// guard and transaction across every page; this reader adds no late row lock.
type ProjectRepositoryReader interface {
	ProjectRepositories(ctx context.Context, projectKey string, page kernel.Page) ([]ProjectRepository, string, error)
}

// ProjectRepositoryLookup reads one repository's current owner and environments
// without hydrating configuration secrets or deciding administration authority.
type ProjectRepositoryLookup interface {
	ProjectRepository(ctx context.Context, key string) (ProjectRepository, error)
}
