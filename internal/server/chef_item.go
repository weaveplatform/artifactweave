package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	cheffmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/chef"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Cached metadata describes versions outside an individual caller's permission
// patterns. Only the native API filters and rewrites those documents.
func tryChefItem(
	_ *Kit, w http.ResponseWriter, r *http.Request, _ store.Store, c repo.Config,
	_ bool, req ItemRequest, _ kernel.Principal, _ Access,
) bool {
	if !chefMetadataItem(c, req.Path) {
		return false
	}
	httpkit.WriteErr(w, r, ItemNotFound())
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
