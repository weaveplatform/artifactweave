package gems

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Release is one gem a repository lists: its specification and, for a gem a
// local or federated repository stores, when it was stored, which dates its
// quick gemspec. A gem a remote's upstream index lists has no such time.
type Release struct {
	Package
	Created time.Time
}

// Merger is the RubyGems virtual.Merger: a virtual repository's gems are its
// members' gems, the first member in the walk that lists a gem's file
// supplying it (virtual.UnionBy), so a gem two members hold is rendered from
// the copy the gem download serves (decision 0008, "Per-format merges").
// The indexes then render the merged list as they render one repository's.
type Merger struct{}

// Merge returns the gems of parts, in the walk's order, one per file.
func (Merger) Merge(_ context.Context, parts []virtual.Part[[]Release]) ([]Release, error) {
	docs := make([][]Release, len(parts))
	for i, p := range parts {
		docs[i] = p.Doc
	}
	return virtual.UnionBy(docs, func(r Release) string { return r.Path }), nil
}
