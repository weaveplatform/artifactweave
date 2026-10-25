// Package jetbrainsplugins serves the JetBrains Marketplace host protocol for
// remote JetBrains Plugins repositories. The IDE sends this protocol when
// idea.plugins.host points at Artifactory's api/jetbrainsplugins route.
package jetbrainsplugins

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Protocol exposes only the remote, read-only class approved for JetBrains
// Plugins. IDEs cannot provide repository credentials to a replacement
// Marketplace host, so ordinary anonymous repository read permission applies.
func Protocol(svc *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{}
	if svc != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: svc}
	}
	return pkgroute.Protocol{
		Name: "jetbrainsplugins", Types: []repo.PackageType{repo.JetBrainsPlugins},
		Surface: apierror.Artifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct{ kit *pkgroute.Kit }

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	q := pkgroute.RequestFrom(r)
	resource, archiveID, err := parseResource(q.Repo, q.Rest, r.URL.RawQuery)
	if err != nil {
		h.kit.Error(w, r, err)
		return
	}
	resource.Method = http.MethodGet
	result, err := h.kit.Read(r, resource)
	if err != nil {
		h.kit.Error(w, r, err)
		return
	}
	if archiveID != "" {
		media := ""
		if result.Stream != nil {
			media = result.Stream.MediaType
		} else if result.Item != nil {
			media = result.Item.MimeType
		}
		extension := ".zip"
		if result.Item != nil {
			if final, parseErr := url.Parse(result.Item.RemoteURL); parseErr == nil &&
				safeMarketplaceFile(final.Path, true) {
				extension = path.Ext(final.Path)
			}
		} else if strings.HasPrefix(media, "application/java-archive") ||
			strings.HasPrefix(media, "application/x-java-archive") {
			extension = ".jar"
		}
		// Derived from the validated plugin ID, never an upstream header or URL.
		name := strings.ReplaceAll(archiveID, ".", "-")
		if !safeFilename(archiveID) {
			name = "opaque-" + digest(archiveID)
		}
		filename := "plugin-" + name + extension
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	}
	h.kit.ServeResult(w, r, result)
}
