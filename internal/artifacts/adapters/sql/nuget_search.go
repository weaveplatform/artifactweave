package sql

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// SearchNuGet returns a NuGet search's candidates (port.NuGetSearch), by
// the query npm's search uses (npm_search.go).
func (p packages) SearchNuGet(
	ctx context.Context,
	q port.NuGetSearch,
	pg kernel.Page,
) ([]port.SearchCandidate, string, error) {
	return p.search(ctx, "search nuget packages", q.RepoKey, q.Terms, q.Prefix, pg)
}
