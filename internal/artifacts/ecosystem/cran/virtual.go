package cran

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Merger is the CRAN virtual.Merger: a virtual repository's PACKAGES lists
// each package name once, at the greatest version its members list
// (CompareVersion), from the first member in the walk that lists that
// version, so the row names the copy a GET of the archive through the
// virtual repository serves (decision 0008, "Per-format merges"). 7.x's
// CranVirtualMerger compares the members' versions (RTFACT-20544 shows it
// parsing them) and lists one row per name, "based on the last version"
// (RTFACT-31049). That issue also shows it folding names that differ only
// in case into one row, a bug artifactweave does not mirror: R's package
// names are case sensitive, so each spelling keeps its own row.
type Merger struct{}

// Merge returns one package per name of parts, in the order each name is
// first listed.
func (Merger) Merge(_ context.Context, parts []virtual.Part[[]Package]) ([]Package, error) {
	at := map[string]int{}
	var out []Package
	for _, part := range parts {
		for _, pkg := range part.Doc {
			i, ok := at[pkg.Name]
			switch {
			case !ok:
				at[pkg.Name] = len(out)
				out = append(out, pkg)
			case CompareVersion(pkg.Version, out[i].Version) > 0:
				out[i] = pkg
			}
		}
	}
	return out, nil
}
