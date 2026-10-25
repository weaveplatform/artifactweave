package conan

import (
	"net/http"

	conanfmt "github.com/weaveplatform/artifactweave/internal/format/conan"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func (h handler) deleteRecipe(w http.ResponseWriter, r *http.Request, ref conanfmt.Reference) {
	h.deleteRevision(w, r, ref.Root())
}

func (h handler) deletePackages(w http.ResponseWriter, r *http.Request, ref conanfmt.Reference) {
	h.deleteRevision(w, r, ref.Root()+"/package")
}

func (h handler) deletePackage(w http.ResponseWriter, r *http.Request, f conanfmt.FilePath) {
	h.deleteRevision(w, r, f.Root())
}

func (h handler) deleteRevision(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodDelete {
		h.method(w, r)
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		h.method(w, r)
		return
	}
	if err := h.k.DeleteTree(r, path); err != nil {
		h.error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
