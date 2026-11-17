package pub

import (
	"context"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Merger is the Dart pub virtual.Merger over a format's releases of one
// package, which Version names: a virtual repository's versions document
// lists every version its members list, the first member in the walk that
// lists a version supplying it (virtual.UnionBy), so a version two members
// hold is described, and its archive served, as that member has it
// (decision 0008, "Per-format merges"). The merged list is ordered as one
// repository's (SortNewestFirst).
type Merger[T any] struct {
	Version func(T) string
}

// Merge returns the releases of parts, one per version, newest first.
func (m Merger[T]) Merge(_ context.Context, parts []virtual.Part[[]T]) ([]T, error) {
	docs := make([][]T, len(parts))
	for i, p := range parts {
		docs[i] = p.Doc
	}
	out := virtual.UnionBy(docs, m.Version)
	SortNewestFirst(out, m.Version)
	return out, nil
}

// SortNewestFirst orders releases by version, newest first, as SemVer 2.0
// orders them (JFrog's pub page serves versions by SemVer 2.0 rules), and
// versions SemVer counts equal, which differ only in build metadata, by
// their text, so an answer never depends on the order members or the store
// listed them in.
func SortNewestFirst[T any](releases []T, version func(T) string) {
	virtual.SortVersions(releases, version, func(a, b string) int {
		if c := semver.Compare("v"+a, "v"+b); c != 0 {
			return -c
		}
		return strings.Compare(a, b)
	})
}
