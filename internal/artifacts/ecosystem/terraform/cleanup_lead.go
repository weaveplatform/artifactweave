package terraform

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead follows module spelling preference and preserves all admitted provider platform archives; signatures are not archive leads.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		v := file.Version
		if m, ok := ParseModule(file.Path); ok {
			lead := api.IndexLead{Found: m.Package() == v.Package.Name && m.Version == v.Version, Single: true, TieBreak: file.Path}
			if !m.V {
				lead.Rank = 1
			}
			return lead
		}
		f, ok := ParseProviderPackageFile(file.Path)
		return api.IndexLead{Found: ok && f.Kind == KindArchive && f.Package() == v.Package.Name && f.Version == v.Version}
	}, nil
}
