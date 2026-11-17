package bower

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Names merges the package names each member of a virtual repository lists
// into the virtual repository's package list and search: every name once,
// sorted, as the list of one repository is.
type Names struct{}

// Merge returns the union of the parts' names, sorted.
func (Names) Merge(_ context.Context, parts []virtual.Part[[]string]) ([]string, error) {
	out := virtual.UnionBy(docs(parts), func(name string) string { return name })
	slices.Sort(out)
	return out, nil
}

// Versions merges the versions of one package that each member of a
// virtual repository holds into the versions the virtual repository
// resolves the package to: each version once, in the walk's order, so the
// first member that holds a version is the one its archive comes from
// (decision 0008, R1). The order is not sorted: bower-art-resolver reads
// the refs as tags and picks by semantic version itself.
type Versions struct{}

// Merge returns the union of the parts' versions in the walk's order.
func (Versions) Merge(_ context.Context, parts []virtual.Part[[]string]) ([]string, error) {
	return virtual.UnionBy(docs(parts), func(version string) string { return version }), nil
}

func docs(parts []virtual.Part[[]string]) [][]string {
	out := make([][]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.Doc)
	}
	return out
}
