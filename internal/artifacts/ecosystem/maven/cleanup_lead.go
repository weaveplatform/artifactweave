package maven

import (
	"path"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead uses the same snapshot POM order as version metadata. Linked
// artifacts can share a digest across builds; a digest alone is not identity.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		build, ok := pomPriority(file.Path)
		return api.IndexLead{Found: ok, Single: true, Rank: int64(build), TieBreak: file.Path}
	}, nil
}

// CleanupLeadWithLayout uses the configured descriptor pattern for Ivy/SBT and
// custom layouts. Treating every XML file as a descriptor would select metadata
// or a descriptor belonging to another module. Unknown layouts select no lead.
func (h Handler) CleanupLeadWithLayout(l *layout.Layout, v model.PackageVersion) (format.CleanupLeadSelector, error) {
	if l != nil && !l.Definition().DistinctiveDescriptorPathPattern {
		return h.CleanupLead(v)
	}
	return func(file api.IndexFile) api.IndexLead {
		lead := api.IndexLead{Single: true, Rank: -1, TieBreak: file.Path}
		lead.Found = l != nil && l.IsDescriptor(file.Path)
		if lead.Found {
			if build, ok := ParseUniqueBuild(path.Base(file.Path)); ok {
				lead.Rank = int64(build.Number)
			}
		}
		return lead
	}, nil
}
