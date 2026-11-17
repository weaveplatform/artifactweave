package pypihttp

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (h handler) virtualUpload(w http.ResponseWriter, r *http.Request) {
	v := pkgroute.RequestFrom(r).Repo
	rest := pkgroute.RequestFrom(r).Rest
	// RTFACT-23876 reports 415 at the install URL. Its message and the
	// corresponding local answer remain unsettled; retain the PYP-02 fallback.
	switch strings.TrimSuffix(rest, "/") {
	case "simple":
		h.k.Error(w, r, apierror.New(http.StatusUnsupportedMediaType, "Unsupported Media Type"))
		return
	case "":
	default:
		h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed, "Twine is allowed to deploy packages only to the base level"))
		return
	}
	if mr := h.virtualDeployment(w, r); mr != nil {
		h.uploadTo(w, mr, &v)
	}
}

func (h handler) virtualPut(w http.ResponseWriter, r *http.Request) {
	v := pkgroute.RequestFrom(r).Repo
	mr := h.virtualDeployment(w, r)
	if mr == nil {
		return
	}
	path, err := pkgroute.ParsePath(pkgroute.RequestFrom(mr).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !h.virtualDeployAllowed(w, mr, v, path.String(), path.Folder) {
		return
	}
	h.k.DeployItem(w, mr)
}

// The shared mapping supplies R6's target and error. Effective membership
// additionally rejects stale, unrelated targets while retaining a legacy DDR
// reachable through a nested virtual. Save-time validation is stricter.
func (h handler) virtualDeployment(w http.ResponseWriter, r *http.Request) *http.Request {
	v := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	if errors.Is(err, virtual.ErrNoDeployTarget) {
		h.k.Error(w, r, itemkit.NoDeployTarget(v.Key))
		return nil
	}
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("pypi deployment target: %w", err))
		return nil
	}
	members, err := virtual.Members(r.Context(), h.k.DB().Repos(), v, virtual.Options{Order: virtual.Stored})
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("pypi deployment members: %w", err))
		return nil
	}
	if !repo.AcceptsMember(v.PackageType, target.PackageType) || !slices.ContainsFunc(members, func(m virtual.Member) bool { return m.Repo.Key == target.Key }) {
		h.k.Error(w, r, itemkit.NoDeployTarget(v.Key))
		return nil
	}
	mr, err := h.k.MemberRequest(r, target)
	if err != nil {
		h.k.Error(w, r, err)
		return nil
	}
	return mr
}

func (h handler) virtualDeployAllowed(w http.ResponseWriter, r *http.Request, v repo.Config, path string, folder bool) bool {
	allowed := h.k.Can(r, path, api.ActionWrite)
	if folder {
		allowed = h.k.CanFolder(r, path, api.ActionWrite)
	}
	if allowed {
		return true
	}
	h.k.Error(w, r, virtualDeployRefusal(r, v, path))
	return false
}

func virtualDeployRefusal(r *http.Request, v repo.Config, path string) error {
	caller, ok := kernel.FromContext(r.Context())
	if !ok {
		caller = kernel.Anonymous
	}
	// R4 names the caller's virtual path, never a hidden deployment target.
	return fmt.Errorf("pypi deployment authorization: %w", itemkit.VirtualRefused(v, path, caller))
}
