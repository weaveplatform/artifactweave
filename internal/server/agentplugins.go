package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/agentplugins"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// registerAgentMarketplaceRoutes serves the agent plugin marketplace
// documents. HEAD shares the GET handler, as on the item paths.
func (s artifactoryAPI) registerAgentMarketplaceRoutes(r chi.Router) {
	r.Get("/artifactory/api/agentplugins/{repoKey}/{marketplace}", s.getAgentMarketplace)
	r.Head("/artifactory/api/agentplugins/{repoKey}/{marketplace}", s.getAgentMarketplace)
}

func (s artifactoryAPI) getAgentMarketplace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.Content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	c, cached, err := itemkit.ResolveRepo(ctx, st, chi.URLParam(r, "repoKey"))
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve repository", err))
		return
	}
	name := chi.URLParam(r, "marketplace")
	if c.PackageType != repo.AgentPlugins || c.Class != repo.Local || cached ||
		!agentplugins.IsMarketplace(name) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	agentPluginItems{s.Kit}.serveAgentMarketplace(w, r, st, c, name, p, acc)
}

// agentPluginItems serves the agent plugin marketplace documents over the
// item kit.
type agentPluginItems struct{ *itemkit.Kit }

func tryAgentMarketplaceItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := agentPluginItems{k}
	if c.PackageType != repo.AgentPlugins || c.Class != repo.Local || cached ||
		req.IsFolder || len(req.Props) != 0 || !agentplugins.IsReservedMarketplace(req.Path) {
		return false
	}
	if !agentplugins.IsMarketplace(req.Path) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return true
	}
	s.serveAgentMarketplace(w, r, st, c, req.Path, p, acc)
	return true
}

func (s agentPluginItems) serveAgentMarketplace(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	name string, p kernel.Principal, acc itemkit.Access,
) {
	if err := generic.CheckDownload(c, name, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "download check", err))
		return
	}
	if !acc.Folder(c, "", types.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	data, err := agentplugins.Marketplace(r.Context(), st, c.Key, name, func(path string) bool {
		return acc.File(c, path, types.ActionRead) && generic.CheckDownload(c, path, false) == nil
	})
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "agent plugin marketplace", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		// json.Marshal HTML-escapes manifest strings; the media type is JSON.
		//nolint:gosec // encoded JSON cannot inject into the HTML response surface
		_, _ = w.Write(data)
	}
}
