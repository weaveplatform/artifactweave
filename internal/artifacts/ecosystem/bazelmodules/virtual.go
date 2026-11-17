package bazelmodules

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// VersionMetadata is a Bazel registry's per-module version document.
type VersionMetadata struct {
	Versions       []string          `json:"versions"`
	YankedVersions map[string]string `json:"yanked_versions,omitempty"`
	Deprecated     string            `json:"deprecated,omitempty"`
}

// Merger unions authorized metadata, keeping the first member's version state.
type Merger struct{}

// Merge sorts the version union and retains yanking only from winning versions.
func (Merger) Merge(_ context.Context, parts []virtual.Part[VersionMetadata]) (VersionMetadata, error) {
	out := VersionMetadata{Versions: []string{}, YankedVersions: map[string]string{}}
	seen := map[string]bool{}
	for _, p := range parts {
		for _, v := range p.Doc.Versions {
			if seen[v] {
				continue
			}
			seen[v] = true
			out.Versions = append(out.Versions, v)
			if reason, ok := p.Doc.YankedVersions[v]; ok {
				out.YankedVersions[v] = reason
			}
			if out.Deprecated == "" {
				out.Deprecated = p.Doc.Deprecated
			}
		}
	}
	slices.Sort(out.Versions)
	return out, nil
}
