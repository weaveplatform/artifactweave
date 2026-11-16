package artifactshttp

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	ml "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/machinelearning"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
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

// TryMachineLearningItem is the item overlay (itemkit.Overlay) that answers
// the Machine Learning formats' private records, which only their own
// protocol serves, as files the repository does not hold
// (itemkit.NotServed, #1599). It writes nothing and returns false for
// every other path.
func TryMachineLearningItem(
	_ *itemkit.Kit, w http.ResponseWriter, r *http.Request, _ itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	if !machineLearningPrivate(c, req.Path) {
		return false
	}
	httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
	return true
}
