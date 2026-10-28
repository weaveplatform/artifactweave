package server

import (
	"net/http"
	"strings"

	cheffmt "github.com/weaveplatform/artifactweave/internal/format/chef"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
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
	writeErr(w, r, itemNotFound())
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
