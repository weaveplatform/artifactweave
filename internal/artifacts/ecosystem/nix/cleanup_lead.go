package nix

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead uses the existing store path's narinfo, never its NAR or a channel archive.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		hash, ok := infoHash(file.Path)
		return api.IndexLead{Found: ok && hash == file.Version.Version, Single: true, TieBreak: file.Path}
	}, nil
}
