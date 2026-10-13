// Package server builds the single chi router that serves every JFrog path
// prefix: /artifactory, /access, /event, /router, /jfconnect and /v2. Flat
// CRUD families come from the generated strict servers; item paths and
// protocol handlers are hand-written and mounted first (decision 0002).
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/internal/server/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/internal/server/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/internal/server/gen/grid"
	pfedgen "github.com/weaveplatform/artifactweave/internal/server/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/internal/server/gen/worker"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Config is what the router needs from the application configuration.
type Config struct {
	// BaseURL is the operator-configured public base URL; empty means derive
	// it from the request. Override and X-Forwarded-* headers take precedence.
	BaseURL string
	// CompatVersion is the Artifactory version reported to clients; it gates
	// client features, so it is configuration rather than the build version.
	CompatVersion string
	// Revision is the real artifactweave build identifier.
	Revision string
	// Logger receives access and panic logs; nil falls back to slog.Default so
	// tests and library users need not wire one.
	Logger *slog.Logger
	// Store is the metadata store and Blobs the blob store that the content
	// handlers read and write. ColdBlobs is the archive tier, nil when none
	// is configured.
	Store     store.Store
	Blobs     blob.Store
	ColdBlobs blob.Store
	// Auth verifies credentials. Nil serves every route without
	// authentication, which only tests do: such a handler has no store and
	// answers just the system endpoints and the stubs.
	Auth *auth.Authenticator
}

// managementBodyLimit bounds JSON management requests. 64 MiB covers the
// largest documented payloads (batch repository creation, build-info) with
// room to spare; artifact uploads never pass through it.
const managementBodyLimit = 64 << 20

// New returns the fully wired handler.
func New(cfg Config) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	r := chi.NewRouter()
	r.Use(
		requestID,
		recoverPanic(logger),
		accessLog(logger),
		baseurl.Middleware(cfg.BaseURL),
		normalizeContentType,
		repositoriesSlash,
	)
	if cfg.Auth != nil {
		r.Use(authenticate(cfg.Auth))
	}
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)

	artSvc := artifactoryService{cfg: cfg}
	accSvc := accessService{cfg: cfg}

	// Hand-written routes go first so they win over anything a generated mux
	// might later claim for the same prefix.
	r.Get("/artifactory/api/system/ping", pingHandler)
	r.Post("/artifactory/api/system/usage", sinkHandler)
	r.Post("/jfconnect/api/v1/backoffice/metrics/log", sinkHandler)
	r.Get("/access/api/v1/system/ping", pingHandler)
	r.Get("/router/api/v1/system/health", routerHealth)
	r.With(bodyLimit(managementBodyLimit)).
		Patch("/artifactory/api/system/configuration", artSvc.patchConfiguration)
	artSvc.registerItemRoutes(r)

	// Products artifactweave does not implement. 501 names the product;
	// Mission Control is a plain 404 because clients treat an absent service
	// as "not installed" (research 10). Distribution, Workers, federation and
	// topology are in scope (research 16): they have specs, and every
	// operation is routed individually below, answering 501 until built.
	r.HandleFunc("/xray/*", notSupported("Xray"))
	r.HandleFunc("/lifecycle/*", notSupported("Release Lifecycle Management"))
	r.HandleFunc("/apptrust/*", notSupported("AppTrust"))
	r.HandleFunc("/evidence/*", notSupported("Evidence"))
	r.HandleFunc("/mc/*", notFound)

	// Generated strict servers, one per spec, each under its service prefix
	// with the management body limit.
	r.Group(func(g chi.Router) {
		g.Use(bodyLimit(managementBodyLimit), requestDetails)
		strictOpts := artgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  bindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: bindingError(http.StatusInternalServerError),
		}
		artgen.HandlerWithOptions(
			artgen.NewStrictHandlerWithOptions(artSvc, nil, strictOpts),
			artgen.ChiServerOptions{
				BaseURL:          "/artifactory",
				BaseRouter:       g,
				ErrorHandlerFunc: bindingError(http.StatusBadRequest),
			},
		)
		accessOpts := accessgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  bindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: bindingError(http.StatusInternalServerError),
		}
		accessgen.HandlerWithOptions(
			accessgen.NewStrictHandlerWithOptions(accSvc, nil, accessOpts),
			accessgen.ChiServerOptions{
				BaseURL:          "/access",
				BaseRouter:       g,
				ErrorHandlerFunc: bindingError(http.StatusBadRequest),
			},
		)
		eventOpts := eventgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  bindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: bindingError(http.StatusInternalServerError),
		}
		eventgen.HandlerWithOptions(
			eventgen.NewStrictHandlerWithOptions(eventService{}, nil, eventOpts),
			eventgen.ChiServerOptions{
				BaseURL:          "/event",
				BaseRouter:       g,
				ErrorHandlerFunc: bindingError(http.StatusBadRequest),
			},
		)
		distributionOpts := distributiongen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  bindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: bindingError(http.StatusInternalServerError),
		}
		distributiongen.HandlerWithOptions(
			distributiongen.NewStrictHandlerWithOptions(
				distributionService{},
				nil,
				distributionOpts,
			),
			distributiongen.ChiServerOptions{
				BaseURL:          "/distribution",
				BaseRouter:       g,
				ErrorHandlerFunc: bindingError(http.StatusBadRequest),
			},
		)
		workerOpts := workergen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  bindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: bindingError(http.StatusInternalServerError),
		}
		workergen.HandlerWithOptions(
			workergen.NewStrictHandlerWithOptions(workerService{}, nil, workerOpts),
			workergen.ChiServerOptions{
				BaseURL:          "/worker",
				BaseRouter:       g,
				ErrorHandlerFunc: bindingError(http.StatusBadRequest),
			},
		)
		pfedOpts := pfedgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  bindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: bindingError(http.StatusInternalServerError),
		}
		pfedgen.HandlerWithOptions(
			pfedgen.NewStrictHandlerWithOptions(pfedService{}, nil, pfedOpts),
			pfedgen.ChiServerOptions{
				BaseURL:          "/pfed",
				BaseRouter:       g,
				ErrorHandlerFunc: bindingError(http.StatusBadRequest),
			},
		)
		gridOpts := gridgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  bindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: bindingError(http.StatusInternalServerError),
		}
		gridgen.HandlerWithOptions(
			gridgen.NewStrictHandlerWithOptions(gridService{}, nil, gridOpts),
			gridgen.ChiServerOptions{
				BaseURL:          "/grid",
				BaseRouter:       g,
				ErrorHandlerFunc: bindingError(http.StatusBadRequest),
			},
		)
	})

	// Last, so every real handler above has already claimed its pattern.
	registerHandwrittenStubs(r, logger)

	return r
}
