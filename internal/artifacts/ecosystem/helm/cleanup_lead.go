package helm

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead uses the archive selected by the existing chart index, including
// its newest-modified/ID tie rule, rather than selecting another linked copy.
func (Handler) CleanupLead(v model.PackageVersion) (format.CleanupLeadSelector, error) {
	m, err := DecodeMetadata(v.Metadata)
	if err != nil {
		return nil, err
	}
	return func(file api.IndexFile) api.IndexLead {
		return api.IndexLead{Found: m.Path != "" && file.Path == m.Path, Single: true, TieBreak: file.Path}
	}, nil
}
