package nugethttp

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
)

type deploymentKey struct{}

// The request keeps its caller and client-facing route, while the existing
// push handler authorizes the concrete folder and canonical package path.
func (h handler) virtualDeployment(w http.ResponseWriter, r *http.Request) *http.Request {
	v := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	if errors.Is(err, virtual.ErrNoDeployTarget) {
		h.k.Error(w, r, itemkit.NoDeployTarget(v.Key))
		return nil
	}
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("nuget deployment target: %w", err))
		return nil
	}
	members, err := virtual.Members(r.Context(), h.k.DB().Repos(), v, virtual.Options{Order: virtual.Stored})
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("nuget deployment members: %w", err))
		return nil
	}
	if target.PackageType != repo.NuGet || !slices.ContainsFunc(members, func(m virtual.Member) bool { return m.Repo.Key == target.Key }) {
		h.k.Error(w, r, itemkit.NoDeployTarget(v.Key))
		return nil
	}
	mr, err := h.k.MemberRequest(r.WithContext(context.WithValue(r.Context(), deploymentKey{}, v)), target)
	if err != nil {
		h.k.Error(w, r, err)
		return nil
	}
	return mr
}

func addressedRepo(r *http.Request) repo.Config {
	if v, ok := r.Context().Value(deploymentKey{}).(repo.Config); ok {
		return v
	}
	return pkgroute.RequestFrom(r).Repo
}

func (h handler) requireDeploy(w http.ResponseWriter, r *http.Request, path string, folder bool) bool {
	v, mapped := r.Context().Value(deploymentKey{}).(repo.Config)
	if !mapped {
		if folder {
			return h.k.RequireFolder(w, r, path, api.ActionWrite)
		}
		return h.k.Require(w, r, path, api.ActionWrite)
	}
	allowed := h.k.Can(r, path, api.ActionWrite)
	if folder {
		allowed = h.k.CanFolder(r, path, api.ActionWrite)
	}
	if !allowed {
		caller, ok := kernel.FromContext(r.Context())
		if !ok {
			caller = kernel.Anonymous
		}
		h.k.Error(w, r, itemkit.VirtualRefused(v, path, caller))
	}
	return allowed
}
