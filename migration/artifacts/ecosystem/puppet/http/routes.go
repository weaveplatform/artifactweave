package puppethttp

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	puppethttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/puppet/http"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// PuppetAPI serves Puppet's Forge operations and reindex over the item kit.
type PuppetAPI struct{ *itemkit.Kit }

// RegisterPuppetMetadataRoutes mounts Puppet's Forge v3 module and release
// operations under /artifactory/api/puppet/{repoKey}, served by the Puppet
// protocol, and the Puppet reindex, POST
// /artifactory/api/puppet/{repoKey}/reindex, on r. The reindex refuses any
// query with 400 and answers its text once the reindex has run; it writes
// errors in the artifactory profile.
// Generated Forge operations share the native protocol's authorization,
// metadata validation and delivery checks, just as Warehouse operations do.
// o are the item routes' options (ProtocolOptions), as the protocol's own
// mount takes them.
func (s PuppetAPI) RegisterPuppetMetadataRoutes(r chi.Router, o pkgroute.Options) {
	protocol := chi.NewRouter()
	pkgroute.Mount(
		protocol,
		o,
		puppethttp.Protocol(
			puppethttp.Options{Blobs: s.Blobs, RemoteSources: s.RemoteSources},
		),
	)
	serve := func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, chi.NewRouteContext())
		protocol.ServeHTTP(w, r.WithContext(ctx))
	}
	for _, path := range []string{"/v3/modules", "/v3/modules/{userModule}", "/v3/releases", "/v3/releases/{userModuleVersion}"} {
		r.Get("/artifactory/api/puppet/{repoKey}"+path, serve)
	}
	r.Post(
		"/artifactory/api/puppet/{repoKey}/reindex",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" {
				apierror.Write(
					w,
					apierror.ProfileArtifactory,
					httpkit.BadRequestf("Puppet reindex does not accept query parameters"),
				)
				return
			}
			_, err := s.Reindex(
				r.Context(),
				repo.Puppet,
				chi.URLParam(r, "repoKey"),
				"",
				true,
			)
			if err != nil {
				apierror.Write(w, apierror.ProfileArtifactory, err)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Puppet repository reindexed.\n"))
		},
	)
}
