package server

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	pypihttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/pypi/http"
)

// PypiAPI serves PyPI's Warehouse JSON operations over the item kit.
type PypiAPI struct{ *itemkit.Kit }

// RegisterPypiMetadataRoutes mounts PyPI's Warehouse JSON operations, GET
// /artifactory/api/pypi/{repoKey}/pypi/{package}/json and
// .../{package}/{version}/json, on r, served by the PyPI protocol.
// Warehouse GET operations share the protocol's delivery fences and source
// selection. Register after generated routes, retaining their declared paths.
// o are the item routes' options (ProtocolOptions), as the protocol's own
// mount takes them.
func (s PypiAPI) RegisterPypiMetadataRoutes(r chi.Router, o pkgroute.Options) {
	protocol := chi.NewRouter()
	pkgroute.Mount(
		protocol,
		o,
		pypihttp.Protocol(
			pypihttp.Options{Format: s.FormatSettings.PyPI, RemoteSources: s.RemoteSources},
		),
	)
	serve := func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, chi.NewRouteContext())
		protocol.ServeHTTP(w, r.WithContext(ctx))
	}
	r.Get("/artifactory/api/pypi/{repoKey}/pypi/{package}/json", serve)
	r.Get("/artifactory/api/pypi/{repoKey}/pypi/{package}/{version}/json", serve)
}
