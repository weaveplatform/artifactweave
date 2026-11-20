package terraformbackend

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead follows the index's current-state preference without opening sealed state content.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		f, ok := ParseFile(file.Path)
		v := file.Version
		ms := f.Ms
		lead := api.IndexLead{Single: true, TieBreak: file.Path}
		if f.Latest {
			ms, lead.Rank = file.Created.UnixMilli(), 1
		}
		lead.Found = ok && f.Workspace == v.Package.Name && Version(ms) == v.Version
		return lead
	}, nil
}
