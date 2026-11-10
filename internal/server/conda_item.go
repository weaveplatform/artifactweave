package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	condafmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Stored shards and CAS containers can span several read grants. Their native
// routes construct the representation the current caller is allowed to read.
func TryCondaItem(
	_ *itemkit.Kit, w http.ResponseWriter, r *http.Request, _ store.Store, c repo.Config,
	_ bool, req itemkit.ItemRequest, _ kernel.Principal, _ itemkit.Access,
) bool {
	if !protocolPrivateItem(c, req.Path) {
		return false
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
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
