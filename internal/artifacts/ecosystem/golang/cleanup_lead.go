package golang

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead recognizes the module's admitted .mod file. The parser checks
// canonical package/version identity; a .zip or .info cannot replace the lead.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		f, ok := ParseFile(file.Path)
		v := file.Version
		return api.IndexLead{Found: ok && f.Ext == ExtMod && f.Module == v.Package.Name && f.Version == v.Version, Single: true, TieBreak: file.Path}
	}, nil
}
