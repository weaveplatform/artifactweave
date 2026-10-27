package huggingface

import (
	"net/http"
	"net/url"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
)

type createRepoRequest struct {
	Name         string `json:"name"`
	Organization string `json:"organization"`
	Type         string `json:"type"`
	Visibility   string `json:"visibility,omitempty"`
}

// createRepo acknowledges an authorized Hub namespace. The enclosing
// Artifactory repository is already provisioned; the model or dataset becomes
// visible when its first immutable commit is published.
func (h handler) createRepo(w http.ResponseWriter, r *http.Request) {
	var request createRepoRequest
	if r.URL.RawQuery != "" || boundedJSON(r, 1<<20, &request) != nil {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Hugging Face create request"))
		return
	}
	kind := "models"
	switch request.Type {
	case "", "model":
	case "dataset":
		kind = "datasets"
	default:
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "unsupported Hugging Face repository type"))
		return
	}
	if request.Visibility != "" && request.Visibility != "public" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "Hugging Face visibility is managed by repository permissions"))
		return
	}
	id, ok := format.ParseIdentity(kind, request.Organization, request.Name)
	if !ok {
		h.k.Error(w, r, badIdentity())
		return
	}
	if _, err := h.targetRequest(r, id); err != nil {
		h.k.Error(w, r, err)
		return
	}
	repoPath := id.RepoID()
	if kind == "datasets" {
		repoPath = "datasets/" + repoPath
	}
	repoURL := baseurl.From(r.Context()) + "/artifactory/api/huggingfaceml/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) + "/" + repoPath
	h.writeJSON(w, map[string]string{"url": repoURL})
}
