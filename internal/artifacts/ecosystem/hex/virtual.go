package hex

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Merger merges already-authorized registries under Repository, retaining the
// first member's release and its advisory associations (decision 0008).
type Merger struct{ Repository string }

// Merge unions packages and releases without changing the member documents.
func (m Merger) Merge(_ context.Context, parts []virtual.Part[map[string]RegistryPackage]) (map[string]RegistryPackage, error) {
	out := map[string]RegistryPackage{}
	for _, part := range parts {
		for name, entry := range part.Doc {
			current := out[name]
			current.Name = name
			current.Repository = m.Repository
			seen := map[string]bool{}
			for _, r := range current.Releases {
				seen[r.Version] = true
			}
			offset := uint64(len(current.Advisories))
			added := false
			for _, release := range entry.Releases {
				if seen[release.Version] {
					continue
				}
				seen[release.Version] = true
				mapped := RebindRelease(release, entry.Repository, m.Repository)
				mapped.AdvisoryIndexes = slices.Clone(mapped.AdvisoryIndexes)
				for i := range mapped.AdvisoryIndexes {
					mapped.AdvisoryIndexes[i] += offset
				}
				current.Releases = append(current.Releases, mapped)
				added = true
			}
			if added {
				current.Advisories = append(current.Advisories, entry.Advisories...)
				out[name] = current
			}
		}
	}
	for name, entry := range out {
		out[name] = CompactAdvisories(entry)
	}
	return out, nil
}

// RebindRelease keeps same-registry dependencies behind the selected registry.
func RebindRelease(release Package, from, to string) Package {
	if from == "" || from == to {
		return release
	}
	release.Requirements = slices.Clone(release.Requirements)
	for i := range release.Requirements {
		if release.Requirements[i].Repository == from {
			release.Requirements[i].Repository = to
		}
	}
	return release
}

// CompactAdvisories retains only referenced advisories, remapping indexes
// so denied or shadowed releases disclose no advisory metadata.
func CompactAdvisories(reg RegistryPackage) RegistryPackage {
	reg.Releases = slices.Clone(reg.Releases)
	oldToNew := map[uint64]uint64{}
	var advisories [][]byte
	for i := range reg.Releases {
		indexes := slices.Clone(reg.Releases[i].AdvisoryIndexes)
		for j, old := range indexes {
			mapped, ok := oldToNew[old]
			if !ok {
				mapped = uint64(len(advisories))
				oldToNew[old] = mapped
				advisories = append(advisories, reg.Advisories[old])
			}
			indexes[j] = mapped
		}
		reg.Releases[i].AdvisoryIndexes = indexes
	}
	reg.Advisories = advisories
	return reg
}
