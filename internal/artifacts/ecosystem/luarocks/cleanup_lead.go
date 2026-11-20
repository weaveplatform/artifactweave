package luarocks

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead preserves the admitted rockspec and platform archives, excluding signatures and generated manifests.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		id, ok := Parse(file.Path)
		v := file.Version
		return api.IndexLead{Found: ok && id.Name == v.Package.Name && id.Version == v.Version}
	}, nil
}
