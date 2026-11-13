package agentpluginshttp

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
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
	s.serveAgentMarketplace(w, r, st, c, name, p, acc, false)
}

// AgentPluginItems serves the agent plugin marketplace documents over the
// item kit.
type AgentPluginItems struct{ *itemkit.Kit }

// TryAgentMarketplaceItem is the item kit's overlay for the agent plugin
// marketplace documents. In a local agent plugins repository, a request for
// a marketplace name without properties is answered here, as the API path
// answers it. It answers false, leaving the request to the overlays after
// it and the generic lookup, for anything else, and for a folder that
// stands at a marketplace name: the format stores no file there, so the
// generic lookup answers the folder as it answers any folder. The generic
// lookup would serve the stored file too; the overlay keeps both paths on
// one function, so they cannot answer apart.
func TryAgentMarketplaceItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	if c.PackageType != repo.AgentPlugins || c.Class != repo.Local || cached ||
		req.IsFolder || len(req.Props) != 0 || !agentplugins.IsMarketplace(req.Path) {
		return false
	}
	// A store fault is left to serveAgentMarketplace, which answers it
	// once the caller's permission has been checked.
	if it, err := st.Items().Get(r.Context(), c.Key, false, req.Path); err == nil && it.Folder {
		return false
	}
	AgentPluginItems{k}.serveAgentMarketplace(w, r, st, c, req.Path, p, acc, true)
	return true
}

// serveAgentMarketplace serves the marketplace file name stored at the root
// of c. 7.x stores the files there and serves them as it serves any file,
// so the caller needs Read on the file's own path, and everyone who has it
// reads the same bytes: the same size and checksums an AQL search or the
// storage API reports to them, which JFrog CLI checks a download against.
// itemRoute types the file as the item routes do (ServeItemFile), and the
// API path as the native APIs do (ServeFile).
func (s AgentPluginItems) serveAgentMarketplace(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	name string, p kernel.Principal, acc itemkit.Access, itemRoute bool,
) {
	ctx := r.Context()
	// The read permission first, so a caller who may not read the file
	// learns nothing of how the repository is set (DV-057); then the
	// policy at the file's own path, where 7.x stores it.
	if !acc.File(c, name, api.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if err := generic.CheckDownload(c, name, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "download check", err))
		return
	}
	// The format stores the files from the work queue; resolving the path
	// runs the work a completed write left owed, or queues it for a
	// repository whose files were never stored, so the file read next is
	// current.
	if _, err := s.Items(st).Resolve(ctx, c, format.Download{Path: name}); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "agent plugin marketplace", err))
		return
	}
	it, err := st.Items().Get(ctx, c.Key, false, name)
	switch {
	case errors.Is(err, kernel.ErrNotFound) || err == nil && it.Folder:
		// Without a work queue that runs here nothing has stored the file
		// yet. A folder at the name holds no marketplace file; the item
		// route leaves a folder to the generic lookup, and the API path,
		// which lists no folder, answers it as a missing file.
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	case err != nil:
		httpkit.WriteErr(w, r, s.InternalError(ctx, "agent plugin marketplace", err))
		return
	}
	f := itemkit.Found{Repo: c, Item: it}
	if itemRoute {
		s.ServeItemFile(w, r, st, f, p)
		return
	}
	s.ServeFile(w, r, st, f, p)
}
