package composer

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Releases merges the releases of one package that each member of a
// virtual repository lists into the virtual repository's p2 metadata: each
// version once, from the first member in the walk that lists it, which is
// the member the version's dist is then served from (decision 0008, R1).
// Version is the version of one release.
//
// The order is the walk's, each member's own order kept, so a virtual
// repository of one member lists its versions as that member does. 7.x
// builds a virtual repository's p2 itself, ordered by version
// (RTFACT-30366 shows its order differing from a remote member's); Composer
// 2 reads the list as a set unless it is minified, which these documents
// are not, so the order is left as the walk gives it [I].
type Releases[T any] struct{ Version func(T) string }

// Merge returns the union of the parts' releases by version, the first
// part's release kept for a version two parts list.
func (m Releases[T]) Merge(_ context.Context, parts []virtual.Part[[]T]) ([]T, error) {
	lists := make([][]T, 0, len(parts))
	for _, p := range parts {
		lists = append(lists, p.Doc)
	}
	return virtual.UnionBy(lists, m.Version), nil
}
