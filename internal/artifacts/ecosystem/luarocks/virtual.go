package luarocks

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Entry is one file a member's manifest lists: the rock or rockspec of
// Name's Version for Arch ("rockspec" for the rockspec), with Record, the
// record the member's manifest holds for it, kept as the member wrote it.
type Entry[R any] struct {
	Name, Version, Arch string
	Record              R
}

// Merger is the LuaRocks virtual.Merger: a virtual repository's manifest
// lists every file its members' manifests list, in the walk's order, and
// the first member that lists a file supplies its record, so a rock two
// members hold is listed as the member the rock's download serves it from
// has it (decision 0008, "Per-format merges"). Other versions, revisions
// and architectures of a package come from whichever member lists them, so
// the client sees one repository table, not member URLs.
type Merger[R any] struct{}

// Merge returns the entries of parts, in the walk's order, one per file.
func (Merger[R]) Merge(_ context.Context, parts []virtual.Part[[]Entry[R]]) ([]Entry[R], error) {
	docs := make([][]Entry[R], len(parts))
	for i, p := range parts {
		docs[i] = p.Doc
	}
	type file struct{ name, version, arch string }
	return virtual.UnionBy(docs, func(e Entry[R]) file {
		return file{e.Name, e.Version, e.Arch}
	}), nil
}
