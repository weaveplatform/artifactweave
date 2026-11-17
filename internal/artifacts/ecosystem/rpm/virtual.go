package rpm

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Merger is the RPM virtual.Merger: a virtual repository's repodata
// lists its members' packages, one row per file path, from the first member
// in the walk that lists it, so the row names the copy the package's GET
// through the virtual repository serves (decision 0008, "Per-format
// merges"). The caller encodes and signs the merged rows with the virtual
// repository's own key pair, never a member's.
type Merger struct{}

// Merge returns the packages of parts, in the walk's order, one per path.
func (Merger) Merge(_ context.Context, parts []virtual.Part[[]Package]) ([]Package, error) {
	docs := make([][]Package, len(parts))
	for i, p := range parts {
		docs[i] = p.Doc
	}
	return virtual.UnionBy(docs, func(p Package) string { return p.Path }), nil
}
