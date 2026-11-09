package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	ml "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/machinelearning"
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
	WriteErr(w, r, itemNotFound())
	return true
}
