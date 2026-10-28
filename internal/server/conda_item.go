package server

import (
	"net/http"
	"strings"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Stored shards describe the whole repository. Only the native Conda route
// rebuilds them from records the current caller may read.
func (artifactoryService) tryCondaItem(
	w http.ResponseWriter, r *http.Request, c repo.Config, req itemRequest,
) bool {
	if !condaShardItem(c, req.path) {
		return false
	}
	writeErr(w, r, itemNotFound())
	return true
}

func condaShardItem(c repo.Config, p string) bool {
	if !condafmt.Serves(c.PackageType) {
		return false
	}
	p, _, _ = strings.Cut(p, "!/")
	if artifact, _, ok := item.Sidecar(p); ok {
		p = artifact
	}
	id, ok := condafmt.ParseMetadataPath(p)
	return ok &&
		(id.Filename == condafmt.ShardIndexFilename || condafmt.IsShardFilename(id.Filename))
}
