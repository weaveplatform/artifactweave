package puppet

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Releases merges the releases of one module that each member of a virtual
// repository lists into the virtual repository's release list: each
// version once, from the first member in the walk that lists it (decision
// 0008, R1), from the highest version down, the order every release list
// answers in (model.CompareForge, #1426). Version is the version of one
// release. Refused, when set, reports a release whose archive its member's
// own patterns reject: the version stays listed, and the first later member
// whose copy is not refused supplies it instead, so the listing gives the
// checksum of the member the archive is served from (R5, RTFACT-25571).
type Releases[T any] struct {
	Version func(T) string
	Refused func(T) bool
}

// Merge returns the union of the parts' releases by version.
func (m Releases[T]) Merge(_ context.Context, parts []virtual.Part[[]T]) ([]T, error) {
	refused := func(rel T) bool { return m.Refused != nil && m.Refused(rel) }
	at := map[string]int{}
	var out []T
	for _, p := range parts {
		for _, rel := range p.Doc {
			v := m.Version(rel)
			i, ok := at[v]
			switch {
			case !ok:
				at[v] = len(out)
				out = append(out, rel)
			case refused(out[i]) && !refused(rel):
				out[i] = rel
			}
		}
	}
	virtual.SortVersions(out, m.Version, func(a, b string) int { return model.CompareForge(b, a) })
	return out, nil
}
