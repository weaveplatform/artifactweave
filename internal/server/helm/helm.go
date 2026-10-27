// Package helm serves classic Helm repositories under /api/helm/.
package helm

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Options combines local index rendering with an optional remote source service.
type Options struct {
	Format        helmfmt.Settings
	RemoteSources *source.Service
}

// Protocol serves classic Helm metadata, charts and provenance. Local publishing
// uses the item API; remote repositories expose only reads.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Sources: sources,
		Name:    "helm", Types: []repo.PackageType{repo.Helm}, Surface: apierror.SurfaceArtifactory,
		Reserved: []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, settings: o.Format}
			r.Get("/", h.read)
			r.Head("/", h.read)
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct {
	k        *pkgroute.Kit
	settings helmfmt.Settings
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteRead(w, r, p.String())
		return
	}
	if p.String() != helmfmt.IndexPath {
		h.k.ServeItem(w, r, p.String())
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	f, err := h.k.ResolveItem(r, helmfmt.IndexPath)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if f.Item.Folder {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), f.Item.Actual.SHA256)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("helm: open index: %w", err))
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "application/x-yaml")
	if h.settings.IsRelative() {
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, rc)
		}
		return
	}
	var index helmfmt.Index
	if err := yaml.NewDecoder(rc).Decode(&index); err != nil {
		h.k.Error(w, r, fmt.Errorf("helm: decode index: %w", err))
		return
	}
	prefix := baseurl.From(r.Context()) + "/artifactory/" + url.PathEscape(f.Repo.Key) + "/"
	for name, versions := range index.Entries {
		for i := range versions {
			for j, relative := range versions[i].URLs {
				versions[i].URLs[j] = prefix + relative
			}
		}
		index.Entries[name] = versions
	}
	data, err := helmfmt.EncodeIndex(index)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
