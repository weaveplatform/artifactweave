package pub

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead recognizes the canonical indexed Pub archive.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		v := file.Version
		return api.IndexLead{Found: file.Path == Path(v.Package.Name, v.Version), Single: true, TieBreak: file.Path}
	}, nil
}
