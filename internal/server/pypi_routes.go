package server

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	pypihttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/pypi/http"
)

// Warehouse GET operations share the protocol's delivery fences and source
// selection. Register after generated routes, retaining their declared paths.
func (s artifactoryAPI) registerPypiMetadataRoutes(r chi.Router) {
	protocol := chi.NewRouter()
	pkgroute.Mount(
		protocol,
		s.protocolOptions(),
		pypihttp.Protocol(
			pypihttp.Options{Format: s.cfg.FormatSettings.PyPI, RemoteSources: s.cfg.RemoteSources},
		),
	)
	serve := func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, chi.NewRouteContext())
		protocol.ServeHTTP(w, r.WithContext(ctx))
	}
	r.Get("/artifactory/api/pypi/{repoKey}/pypi/{package}/json", serve)
	r.Get("/artifactory/api/pypi/{repoKey}/pypi/{package}/{version}/json", serve)
}
