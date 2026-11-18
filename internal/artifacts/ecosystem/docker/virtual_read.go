package docker

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// VirtualTagCandidate is a readable tag manifest in one effective member.
// Remote caches retain their remote class: their age does not outrank a local.
type VirtualTagCandidate struct {
	Member virtual.Member
	Item   model.Item
}

// LatestVirtualTag chooses the newest local or federated tag manifest. Input
// order resolves ties, preserving the engine's effective order. Remote members
// are considered separately in resolution order only when this finds no local.
func LatestVirtualTag(candidates []VirtualTagCandidate) (VirtualTagCandidate, bool) {
	var chosen VirtualTagCandidate
	found := false
	for _, candidate := range candidates {
		if candidate.Member.Repo.Class != repo.Local && candidate.Member.Repo.Class != repo.Federated {
			continue
		}
		if !found || candidate.Item.Modified.After(chosen.Item.Modified) {
			chosen, found = candidate, true
		}
	}
	return chosen, found
}

// VirtualReferrersPart is a readable member's own referrers, in its local
// order. The caller checks the subject's image Read before collecting any part.
type VirtualReferrersPart struct {
	Member    virtual.Member
	Manifests []Descriptor
}

// MergeVirtualReferrers retains the first descriptor per digest in effective
// member order. Remote caches and upstreams contribute nothing by default
// (7.161.26's oci.fetch.referrers.from.remote.cache.enabled is false).
func MergeVirtualReferrers(parts []VirtualReferrersPart) []Descriptor {
	out := []Descriptor{}
	seen := map[string]bool{}
	for _, part := range parts {
		if part.Member.Repo.Class != repo.Local && part.Member.Repo.Class != repo.Federated {
			continue
		}
		for _, manifest := range part.Manifests {
			if !seen[manifest.Digest] {
				out = append(out, manifest)
				seen[manifest.Digest] = true
			}
		}
	}
	return out
}
