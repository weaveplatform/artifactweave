package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/agentplugins"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (s artifactoryService) getAgentMarketplace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, chi.URLParam(r, "repoKey"))
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve repository", err))
		return
	}
	name := chi.URLParam(r, "marketplace")
	if c.PackageType != repo.AgentPlugins || c.Class != repo.Local || cached ||
		!agentplugins.IsMarketplace(name) {
		writeErr(w, r, itemNotFound())
		return
	}
	s.serveAgentMarketplace(w, r, st, c, name, p, acc)
}

func (s artifactoryService) tryAgentMarketplaceItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	if c.PackageType != repo.AgentPlugins || c.Class != repo.Local || cached ||
		req.folder || len(req.props) != 0 || !agentplugins.IsReservedMarketplace(req.path) {
		return false
	}
	if !agentplugins.IsMarketplace(req.path) {
		writeErr(w, r, itemNotFound())
		return true
	}
	s.serveAgentMarketplace(w, r, st, c, req.path, p, acc)
	return true
}

func (s artifactoryService) serveAgentMarketplace(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	name string, p auth.Principal, acc access,
) {
	if err := generic.CheckDownload(c, name, false); err != nil {
		writeErr(w, r, s.itemError(r.Context(), "download check", err))
		return
	}
	if !acc.folder(c, "", types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	data, err := agentplugins.Marketplace(r.Context(), st, c.Key, name, func(path string) bool {
		return acc.file(c, path, types.ActionRead) && generic.CheckDownload(c, path, false) == nil
	})
	if err != nil {
		writeErr(w, r, s.internalError(r.Context(), "agent plugin marketplace", err))
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
