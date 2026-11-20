package agentpackages

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead recognizes the canonical indexed agent-package archive.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		owner, name, version, ok := ParsePath(file.Path)
		v := file.Version
		return api.IndexLead{Found: ok && owner+"/"+name == v.Package.Name && version == v.Version, Single: true, TieBreak: file.Path}
	}, nil
}
