package huggingface

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
		h.k.Error(
			w, r, apierror.New(http.StatusBadRequest, "unsupported Hugging Face repository type"),
		)
		return
	}
	if request.Visibility != "" && request.Visibility != "public" {
		h.k.Error(
			w,
			r,
			apierror.New(
				http.StatusBadRequest,
				"Hugging Face visibility is managed by repository permissions",
			),
		)
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

type createBranchRequest struct {
	StartingPoint string `json:"startingPoint,omitempty"` //nolint:tagliatelle // Hub wire key.
}

func (h handler) createBranch(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(r)
	branch := chi.URLParam(r, "revision")
	refPath, valid := format.RefPath(id, branch)
	var request createBranchRequest
	if !ok || !valid || r.URL.RawQuery != "" || boundedJSON(r, 1<<20, &request) != nil {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Hugging Face branch request"))
		return
	}
	target, err := h.targetRequest(r, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !h.k.Can(target, refPath, types.ActionWrite) {
		h.k.Error(w, r, apierror.New(http.StatusForbidden, "model branch is not writable"))
		return
	}
	start := request.StartingPoint
	if start == "" {
		start = "main"
	}
	if !format.RefName(start) {
		h.k.Error(w, r, badIdentity())
		return
	}
	parent, err := h.localInfo(target, id, start)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if _, err = h.k.DB().
		Items().
		Get(r.Context(), pkgroute.RequestFrom(target).Repo.Key, false, refPath); err == nil {
		h.k.Error(w, r, apierror.New(http.StatusConflict, "Hugging Face branch already exists"))
		return
	} else if !errors.Is(
		err,
		store.ErrNotFound,
	) {
		h.k.Error(w, r, fmt.Errorf("huggingface: read branch: %w", err))
		return
	}
	if _, err = h.k.Store(
		target,
		refPath,
		bytes.NewReader([]byte(parent.info.SHA)),
		nil,
		types.Checksums{},
	); err != nil {
		h.k.Error(w, r, fmt.Errorf("huggingface: create branch: %w", err))
		return
	}
	w.WriteHeader(http.StatusOK)
}
