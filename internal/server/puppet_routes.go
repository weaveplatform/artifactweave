package server

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	puppethttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/puppet/http"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// Generated Forge operations share the native protocol's authorization,
// metadata validation and delivery checks, just as Warehouse operations do.
func (s artifactoryService) registerPuppetMetadataRoutes(r chi.Router) {
	protocol := chi.NewRouter()
	pkgroute.Mount(
		protocol,
		s.protocolOptions(),
		puppethttp.Protocol(
			puppethttp.Options{Blobs: s.cfg.Blobs, RemoteSources: s.cfg.RemoteSources},
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
			_, err := s.reindex(
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
