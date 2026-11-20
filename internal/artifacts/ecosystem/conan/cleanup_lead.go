package conan

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead preserves each committed recipe revision's conanfile.py; binary and commit metadata are not leads.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		f, ok := ParseStored(file.Path)
		v := file.Version
		name := f.Ref.Name + "@" + f.Ref.User + "/" + f.Ref.Channel
		return api.IndexLead{Found: ok && !f.Package && f.File == "conanfile.py" && name == v.Package.Name && f.Ref.Version == v.Version}
	}, nil
}
