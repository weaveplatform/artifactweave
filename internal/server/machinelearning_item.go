package server

import (
	"net/http"
	"strings"

	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func machineLearningPrivate(c repo.Config, p string) bool {
	if !ml.Serves(c.PackageType) {
		return false
	}
	p, _, _ = strings.Cut(p, "!/")
	if file, _, ok := item.Sidecar(p); ok {
		p = file
	}
	return ml.Private(p)
}

func (artifactoryService) tryMachineLearningItem(
	w http.ResponseWriter,
	r *http.Request,
	c repo.Config,
	req itemRequest,
) bool {
	if !machineLearningPrivate(c, req.path) {
		return false
	}
	writeErr(w, r, itemNotFound())
	return true
}
