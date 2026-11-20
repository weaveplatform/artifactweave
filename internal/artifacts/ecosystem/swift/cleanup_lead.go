package swift

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead recognizes the immutable canonical source archive of an indexed registry release.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		p, ok := ParseArchivePath(file.Path)
		v := file.Version
		return api.IndexLead{Found: ok && p.ID() == v.Package.Name && p.Version == v.Version, Single: true, TieBreak: file.Path}
	}, nil
}
