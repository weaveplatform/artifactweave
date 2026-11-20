package maven

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
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
