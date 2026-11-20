package debian

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead preserves every architecture's admitted binary as a lead; the
// package index has already established its package and version identity.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		return api.IndexLead{Found: ValidBinaryPath(file.Path) && isBinary(file.Path)}
	}, nil
}
