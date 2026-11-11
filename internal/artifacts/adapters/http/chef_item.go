package artifactshttp

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	cheffmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// TryChefItem is the item overlay (itemkit.Overlay) that answers 404 for a
// Chef repository's metadata documents, which only the native API serves:
// cached metadata describes versions outside an individual caller's
// permission patterns, and only the native API filters and rewrites those
// documents. It writes nothing and returns false for every other path.
func TryChefItem(
	_ *itemkit.Kit, w http.ResponseWriter, r *http.Request, _ itemkit.Store, c repo.Config,
	_ bool, req itemkit.ItemRequest, _ kernel.Principal, _ itemkit.Access,
) bool {
	if !chefMetadataItem(c, req.Path) {
		return false
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
	return true
}

func chefMetadataItem(c repo.Config, p string) bool {
	if !cheffmt.Serves(c.PackageType) {
		return false
	}
	p, _, _ = strings.Cut(p, "!/")
	if artifact, _, ok := item.Sidecar(p); ok {
		p = artifact
	}
	return cheffmt.IsMetadata(p)
}
