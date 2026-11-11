package artifactshttp

import (
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	format "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/huggingface"
)

// CAS containers can bundle files with different read grants. The native Xet
// routes authorize and extract only the chunks of a published, readable file.
func protocolPrivateItem(c repo.Config, path string) bool {
	if condaShardItem(c, path) {
		return true
	}
	if !format.Serves(c.PackageType) {
		return false
	}
	path, _, _ = strings.Cut(path, "!/")
	if artifact, _, ok := item.Sidecar(path); ok {
		path = artifact
	}
	return format.IsXetPath(path)
}
