package npmhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// deploymentKey retains the client-facing repository while the local handlers
// authorize and store against its concrete deployment target. It never changes
// the caller, route, download policy or merged-model cache.
type deploymentKey struct{}

func (h handler) deploymentRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	v := pkgroute.RequestFrom(r).Repo
	if v.Class != repo.Virtual {
		return r, true
	}
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	if errors.Is(err, virtual.ErrNoDeployTarget) {
		h.k.Error(w, r, npmNoDeployTarget(v.Key))
		return nil, false
	}
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("npm deployment target: %w", err))
		return nil, false
	}
	members, err := virtual.Members(r.Context(), h.k.DB().Repos(), v, virtual.Options{Order: virtual.Stored})
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("npm deployment members: %w", err))
		return nil, false
	}
	if !repo.AcceptsMember(v.PackageType, target.PackageType) || !slices.ContainsFunc(members, func(m virtual.Member) bool { return m.Repo.Key == target.Key }) {
		h.k.Error(w, r, npmNoDeployTarget(v.Key))
		return nil, false
	}
	ctx := context.WithValue(r.Context(), deploymentKey{}, v)
	mr, err := h.k.MemberRequest(r.WithContext(ctx), target)
	if err != nil {
		h.k.Error(w, r, err)
		return nil, false
	}
	return mr, true
}

// RTFACT-10646 reports npm E400 for a virtual without a deployment target.
// Its exact client body is unavailable: retain R6's wording at npm's 400 [I],
// rather than the item API's 405 (research 25; #216).
func npmNoDeployTarget(key string) error {
	return apierror.New(http.StatusBadRequest, fmt.Sprintf("No local repository was configured as local deployment repository for the (%s) virtual repository.", key))
}

func (h handler) require(w http.ResponseWriter, r *http.Request, path string, action api.Actions) bool {
	if h.k.Can(r, path, action) {
		return true
	}
	h.deploymentRefused(w, r, path)
	return false
}

func (h handler) requireFolder(w http.ResponseWriter, r *http.Request, path string, action api.Actions) bool {
	if h.k.CanFolder(r, path, action) {
		return true
	}
	h.deploymentRefused(w, r, path)
	return false
}

func (h handler) deploymentRefused(w http.ResponseWriter, r *http.Request, path string) {
	caller, ok := kernel.FromContext(r.Context())
	if !ok {
		caller = kernel.Anonymous
	}
	v, mapped := r.Context().Value(deploymentKey{}).(repo.Config)
	if !mapped {
		v = pkgroute.RequestFrom(r).Repo
		// Local repositories retain their existing refusal even when their
		// own configuration carries the virtual-only hiding setting.
		v.HideUnauthorizedResources = nil
	}
	h.k.Error(w, r, itemkit.VirtualRefused(v, path, caller))
}

// npm reads the writable document before deprecate and unpublish. Restricting
// it to the deployment target keeps other members out of a write-back [I].
// The ordinary local read still checks Read before document policy and emits
// virtual tarball URLs through deploymentKey; no merged model is invalidated.
func (h handler) virtualWriteDocument(w http.ResponseWriter, r *http.Request, name string) {
	mr, ok := h.deploymentRequest(w, r)
	if !ok || !h.requireFolder(w, mr, name, api.ActionRead) {
		return
	}
	h.packument(w, mr, name)
}
