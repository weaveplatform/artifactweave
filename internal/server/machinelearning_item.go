package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	ml "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/machinelearning"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
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

func tryMachineLearningItem(
	_ *itemkit.Kit, w http.ResponseWriter, r *http.Request, _ store.Store, c repo.Config,
	_ bool, req itemkit.ItemRequest, _ kernel.Principal, _ itemkit.Access,
) bool {
	if !machineLearningPrivate(c, req.Path) {
		return false
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
	return true
}
