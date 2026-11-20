package machinelearning

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead uses the committed model manifest; staged transactions and model data files cannot replace it.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		v := file.Version
		id := Identity{Name: v.Package.Name, Version: v.Version}
		return api.IndexLead{Found: Name(id.Name) && Version(id.Version) && file.Path == id.Manifest(), Single: true, TieBreak: file.Path}
	}, nil
}
