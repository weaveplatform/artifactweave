package server

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/pypi"
)

// Warehouse GET operations share the protocol's delivery fences and source
// selection. Register after generated routes, retaining their declared paths.
func (s artifactoryService) registerPypiMetadataRoutes(r chi.Router) {
	protocol := chi.NewRouter()
	pkgroute.Mount(
		protocol,
		s.protocolOptions(),
		pypi.Protocol(
			pypi.Options{Format: s.cfg.FormatSettings.PyPI, RemoteSources: s.cfg.RemoteSources},
		),
	)
	serve := func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, chi.NewRouteContext())
		protocol.ServeHTTP(w, r.WithContext(ctx))
	}
	r.Get("/artifactory/api/pypi/{repoKey}/pypi/{package}/json", serve)
	r.Get("/artifactory/api/pypi/{repoKey}/pypi/{package}/{version}/json", serve)
}
