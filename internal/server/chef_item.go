package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	cheffmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/chef"
)

// Cached metadata describes versions outside an individual caller's permission
// patterns. Only the native API filters and rewrites those documents.
func (artifactoryService) tryChefItem(
	w http.ResponseWriter,
	r *http.Request,
	c repo.Config,
	req itemRequest,
) bool {
	if !chefMetadataItem(c, req.path) {
		return false
	}
	WriteErr(w, r, itemNotFound())
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
