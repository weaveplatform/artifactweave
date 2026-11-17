package swift

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Releases merges the release versions of one package that each member of
// a virtual repository lists into the virtual repository's release list
// (the registry's GET /{scope}/{name}): each version once, from the first
// member in the walk that lists it, which is the member its metadata,
// manifest and archive then come from (decision 0008, R1). The registry
// answers the list as a JSON object keyed by version, so the order is not
// the client's to see.
type Releases struct{}

// Merge returns the union of the parts' versions in the walk's order.
func (Releases) Merge(_ context.Context, parts []virtual.Part[[]string]) ([]string, error) {
	lists := make([][]string, 0, len(parts))
	for _, p := range parts {
		lists = append(lists, p.Doc)
	}
	return virtual.UnionBy(lists, func(version string) string { return version }), nil
}
