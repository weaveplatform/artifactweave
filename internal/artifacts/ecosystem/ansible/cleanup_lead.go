package ansible

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead recognizes the admitted collection archive, never its metadata or signatures.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		namespace, name, version, ok := ParsePath(file.Path)
		v := file.Version
		return api.IndexLead{Found: ok && namespace+"."+name == v.Package.Name && version == v.Version, Single: true, TieBreak: file.Path}
	}, nil
}
