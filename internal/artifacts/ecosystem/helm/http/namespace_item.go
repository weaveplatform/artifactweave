package helmhttp

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	helmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// TryHelmNamespaceItem serves the member-qualified item URLs emitted by an
// absolute virtual index. Unqualified paths retain the ordinary item walk.
func TryHelmNamespaceItem(
	k *itemkit.Kit,
	w http.ResponseWriter,
	r *http.Request,
	_ itemkit.Store,
	c repo.Config,
	_ bool,
	req itemkit.ItemRequest,
	_ kernel.Principal,
	_ itemkit.Access,
) bool {
	if c.Class != repo.Virtual || !helmfmt.Serves(c.PackageType) || c.UseNamespaces == nil || !*c.UseNamespaces || req.IsFolder {
		return false
	}
	pk, err := k.Packages(Protocol(Options{Format: k.FormatSettings.Helm, RemoteSources: k.RemoteSources}), true)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return true
	}
	h := handler{k: pk, settings: k.FormatSettings.Helm, virtualRead: true, item: k}
	// Item controls must not look like caller-supplied upstream query data
	// to the Helm chart reader. Keep the trace context for delivery helpers.
	r = r.Clone(r.Context())
	r.URL.RawQuery = itemkit.RemoteResource(r, req.Path).Query
	r = pk.Bind(r, c)
	member, path, named, err := h.namespace(r, req.Path)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return true
	}
	if !named {
		return false
	}
	h.virtualMemberFile(w, r, path, member)
	return true
}
