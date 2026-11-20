package hex

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead uses the canonical Mix tarball, excluding release metadata and checksums.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		v := file.Version
		p, ok := TarballPath(v.Package.Name, v.Version)
		return api.IndexLead{Found: ok && file.Path == p, Single: true, TieBreak: file.Path}
	}, nil
}
