package chef

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Releases merges the versions of one cookbook that each member of a
// virtual repository lists into the virtual repository's cookbook record:
// each version once, from the first member in the walk that lists it
// (decision 0008, R1), from the highest version down, the order a
// cookbook's versions are listed in (model.CompareForge, #1426). Version is
// the version of one release. Also, when set, hands the first member's
// release each later member's copy of its version, in walk order, so a
// download whose first member's own patterns refuse the archive passes on
// to the next member that holds it (R5).
type Releases[T any] struct {
	Version func(T) string
	Also    func(first, later T) T
}

// Merge returns the union of the parts' releases by version.
func (m Releases[T]) Merge(_ context.Context, parts []virtual.Part[[]T]) ([]T, error) {
	at := map[string]int{}
	var out []T
	for _, p := range parts {
		for _, rel := range p.Doc {
			v := m.Version(rel)
			if i, ok := at[v]; ok {
				if m.Also != nil {
					out[i] = m.Also(out[i], rel)
				}
				continue
			}
			at[v] = len(out)
			out = append(out, rel)
		}
	}
	virtual.SortVersions(out, m.Version, func(a, b string) int { return model.CompareForge(b, a) })
	return out, nil
}
