package inmem

import (
	"cmp"
	"context"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// SearchNpm returns an npm search's candidates (port.NpmSearch).
func (p packages) SearchNpm(
	ctx context.Context,
	q port.NpmSearch,
	pg kernel.Page,
) ([]port.SearchCandidate, string, error) {
	return p.search(ctx, q.RepoKey, q.Terms, "", pg)
}

// search returns a text search's candidates in repoKey whose names start
// with prefix, one page of them by name, each with its versions, testing
// each version as port.SearchMayMatch does, which is what the SQL stores
// test.
func (p packages) search(
	ctx context.Context,
	repoKey string,
	terms []string,
	prefix string,
	pg kernel.Page,
) ([]port.SearchCandidate, string, error) {
	runs := port.SearchRuns(terms)
	var (
		out  []port.SearchCandidate
		next string
	)
	err := p.s.Do(ctx, func() error {
		rows := p.t().All(
			func(_ int64, v model.Package) bool {
				return v.RepoKey == repoKey && strings.HasPrefix(v.Name, prefix)
			},
			func(a, b model.Package) int { return cmp.Compare(a.Name, b.Name) },
		)
		// Every version of the repository's packages in one pass, in the
		// order sorted gives each package's, rather than a pass per package.
		in := make(map[int64]bool, len(rows))
		for _, pkg := range rows {
			in[pkg.ID] = true
		}
		byPackage := map[int64][]model.PackageVersion{}
		for _, v := range p.versions().All(
			func(_ int64, v model.PackageVersion) bool { return in[v.PackageID] },
			func(a, b model.PackageVersion) int {
				return cmp.Or(cmp.Compare(a.OrderKey, b.OrderKey), cmp.Compare(a.Version, b.Version))
			},
		) {
			byPackage[v.PackageID] = append(byPackage[v.PackageID], v)
		}
		var found []port.SearchCandidate
		for _, pkg := range rows {
			vs := byPackage[pkg.ID]
			for _, v := range vs {
				if port.SearchMayMatch(pkg.Name, v.Metadata, runs) {
					found = append(found, port.SearchCandidate{Package: pkg, Versions: vs})
					break
				}
			}
		}
		var err error
		out, next, err = memtx.Paginate(found, pg, false,
			func(c port.SearchCandidate) []any { return []any{c.Package.Name} })
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}
