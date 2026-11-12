package agentpluginshttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentplugins"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// RegisterAgentMarketplaceRoutes serves the agent plugin marketplace
// documents. HEAD shares the GET handler, as on the item paths.
func (s AgentPluginItems) RegisterAgentMarketplaceRoutes(r chi.Router) {
	r.Get("/artifactory/api/agentplugins/{repoKey}/{marketplace}", s.getAgentMarketplace)
	r.Head("/artifactory/api/agentplugins/{repoKey}/{marketplace}", s.getAgentMarketplace)
}

func (s AgentPluginItems) getAgentMarketplace(w http.ResponseWriter, r *http.Request) {
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
	c, _, err := itemkit.ResolveRepo(ctx, st, chi.URLParam(r, "repoKey"))
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve repository", err))
		return
	}
	name := chi.URLParam(r, "marketplace")
	// A remote repository's cache alias resolves to the remote repository,
	// so the class check refuses it too.
	if c.PackageType != repo.AgentPlugins || c.Class != repo.Local ||
		!agentplugins.IsMarketplace(name) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	s.serveAgentMarketplace(w, r, st, c, name, p, acc)
}

// AgentPluginItems serves the agent plugin marketplace documents over the
// item kit.
type AgentPluginItems struct{ *itemkit.Kit }

// TryAgentMarketplaceItem is the item kit's overlay for the agent plugin
// marketplace documents. In a local agent plugins repository, a request for
// a marketplace name without properties is answered here, with the plugins
// the caller may read. It answers false, leaving the request to the
// overlays after it and the generic lookup, for anything else.
func TryAgentMarketplaceItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	if c.PackageType != repo.AgentPlugins || c.Class != repo.Local || cached ||
		req.IsFolder || len(req.Props) != 0 || !agentplugins.IsMarketplace(req.Path) {
		return false
	}
	AgentPluginItems{k}.serveAgentMarketplace(w, r, st, c, req.Path, p, acc)
	return true
}

func (s AgentPluginItems) serveAgentMarketplace(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	name string, p kernel.Principal, acc itemkit.Access,
) {
	// The read permission first, so a caller who may not read the
	// repository learns nothing of how it is set (DV-057); then the
	// policy at the file's own path, where 7.x stores it.
	if !acc.Folder(c, "", api.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if err := generic.CheckDownload(c, name, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "download check", err))
		return
	}
	// 7.x rebuilds the marketplace on upload, listing each plugin's latest
	// version, and serves the stored file, so patterns rejecting that
	// version's ZIP leave it listed and refuse only the ZIP's download.
	// The caller's read permission still picks the version, as rendering
	// per reader does.
	data, err := agentplugins.Marketplace(r.Context(), st, c.Key, name, func(path string) bool {
		return acc.File(c, path, api.ActionRead)
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
