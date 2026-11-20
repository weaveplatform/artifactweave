package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RepositoryIndexPages reads secret-free scope facts for package selection.
// Both adapters implement it; the reader rejects a missing capability rather
// than falling back to whole secret-bearing configuration documents.
type RepositoryIndexPages interface {
	IndexRepository(context.Context, string) (api.IndexRepository, error)
	IndexRepositories(context.Context, api.IndexRepositoryQuery, kernel.Page) ([]api.IndexRepository, string, error)
}

// PackageIndexPages reads existing package/version/file rows in bounded pages.
// Package names, version strings and immutable item IDs are the respective
// continuation keys; comparator rank is recomputed after index changes and must
// never become a continuation key. Properties are projected by requested values.
type PackageIndexPages interface {
	IndexPackages(context.Context, api.IndexRepository, kernel.Page) ([]api.IndexPackage, string, error)
	IndexVersions(context.Context, api.IndexPackage, int, kernel.Page) ([]api.IndexVersion, string, error)
	IndexFiles(context.Context, api.IndexVersion, map[string][]string, kernel.Page) ([]api.IndexFile, string, error)
}
