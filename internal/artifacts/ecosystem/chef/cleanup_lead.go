package chef

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead uses the canonical cookbook archive linked by publication and reindex.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		v := file.Version
		return api.IndexLead{Found: file.Path == Path(v.Package.Name, v.Version), Single: true, TieBreak: file.Path}
	}, nil
}
