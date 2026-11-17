package ansible

import (
	"context"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Releases merges the versions of one collection that each member of a
// virtual repository lists into the virtual repository's Galaxy version
// list: each version once, from the first member in the walk that lists it,
// which is the member its archive is then served from (decision 0008, R1).
// Version is the version of one release.
//
// The list runs from the highest semantic version down, as every Galaxy
// version list does. The sort is stable and SemVer ties versions that
// differ only in build metadata, so those keep the order the walk gives
// them, member by member, each member's in its own list's order.
type Releases[T any] struct{ Version func(T) string }

// Merge returns the union of the parts' releases by version, the first
// part's release kept for a version two parts list.
func (m Releases[T]) Merge(_ context.Context, parts []virtual.Part[[]T]) ([]T, error) {
	lists := make([][]T, 0, len(parts))
	for _, p := range parts {
		lists = append(lists, p.Doc)
	}
	out := virtual.UnionBy(lists, m.Version)
	virtual.SortVersions(out, m.Version, func(a, b string) int {
		return semver.Compare("v"+b, "v"+a)
	})
	return out, nil
}
