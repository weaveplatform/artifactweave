package docker

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead follows FolderManifest's canonical tag manifest preference.
// Layers and configuration blobs cannot substitute for an absent manifest.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		v := file.Version
		dir := v.Package.Name + "/" + v.Version + "/"
		out := api.IndexLead{Single: true, TieBreak: file.Path}
		switch file.Path {
		case dir + ManifestFile:
			out.Found, out.Rank = true, 1
		case dir + ListManifestFile:
			out.Found = true
		}
		return out
	}, nil
}
