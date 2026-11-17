package conda

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Merger merges the channel indexes of one subdir that each member of a
// virtual repository serves into the virtual repository's: each package
// file once, from the first member in the walk that lists it (decision
// 0008, R1), which is the member a download of that file is then served
// from. The .tar.bz2 and .conda records merge apart, as the index keeps
// them. The CEP-16 shard index and shards are encoded from the merged
// records (EncodeShards), so this is their merge too: a shard of the
// virtual repository holds every record of its package name the merge
// keeps, and never a member's shard as that member built it.
type Merger struct{ Subdir string }

// Merge returns the merged index of the parts.
func (m Merger) Merge(_ context.Context, parts []virtual.Part[Repodata]) (Repodata, error) {
	out := EmptyRepodata(m.Subdir)
	for _, p := range parts {
		for name, rec := range p.Doc.Packages {
			if _, ok := out.Packages[name]; !ok {
				out.Packages[name] = rec
			}
		}
		for name, rec := range p.Doc.PackagesConda {
			if _, ok := out.PackagesConda[name]; !ok {
				out.PackagesConda[name] = rec
			}
		}
	}
	return out, nil
}
