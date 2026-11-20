package cocoapods

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead uses the canonical pod archive, excluding podspec and signature metadata.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		v := file.Version
		return api.IndexLead{Found: file.Path == Path(v.Package.Name, v.Version), Single: true, TieBreak: file.Path}
	}, nil
}
