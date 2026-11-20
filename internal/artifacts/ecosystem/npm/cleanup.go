package npm

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead recognizes the same canonical tarball linked by publish/index.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		version := file.Version
		return api.IndexLead{
			Found:  file.Path == CanonicalPath(version.Package.Name, version.Version),
			Single: true, TieBreak: file.Path,
		}
	}, nil
}
