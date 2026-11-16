package artifactshttp

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// TryCondaItem is the item overlay (itemkit.Overlay) that answers the paths
// PrivateItem names as files the repository does not hold
// (itemkit.NotServed, #1599): stored shards and CAS containers can span
// several read grants, so only their native routes construct the
// representation the current caller is allowed to read. 7.x keeps a Conda
// repository's package shards under .jfrog/shards/<...>/package_shards/
// (CondaShardsCleanupServiceImpl, 7.161.26), not at the path the protocol
// names, so the item route finds no file there [I for the shard index]. It
// writes nothing and returns false for every other path.
func TryCondaItem(
	_ *itemkit.Kit, w http.ResponseWriter, r *http.Request, _ itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	if !protocolPrivateItem(c, req.Path) {
		return false
	}
	httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
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
