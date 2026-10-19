// Package server builds the single chi router that serves every JFrog path
// prefix: /artifactory, /access, /event, /router, /jfconnect and /v2. Flat
// CRUD families come from the generated strict servers; item paths and
// protocol handlers are hand-written and mounted first (decision 0002).
package server

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/internal/server/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/internal/server/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/internal/server/gen/grid"
	pfedgen "github.com/weaveplatform/artifactweave/internal/server/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/internal/server/gen/worker"
	"github.com/weaveplatform/artifactweave/internal/server/golang"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	// GC is the garbage collector the runtime schedules; POST
	// /api/system/storage/gc runs the same one. Nil builds one per request,
	// for tests.
	GC *gc.Collector
	// BlobLocation describes the hot blob store in the storage summary.
	BlobLocation usage.Location
	// Auth verifies credentials. Nil serves every route without
	// authentication, which only tests do: such a handler has no store and
	// answers just the system endpoints and the stubs.
	Auth *auth.Authenticator
	// Mirrors administers the blob mirrors; nil when no tier is a mirror.
	Mirrors Mirrors
	// Multipart serves the JFrog CLI's multipart upload protocol; nil
	// reports it unsupported, so the CLI uploads in one request.
	Multipart *multipart.Service
	// Tokens issues, refreshes and revokes access tokens for the token API;
	// nil answers that API 501.
	Tokens *token.Service
	// AllowBasicAuthTokens lets a password sent with Basic create tokens
	// through POST /access/api/v1/tokens (token.allow_basic_auth); the zero
	// value refuses it, as Artifactory 7.x does by default.
	AllowBasicAuthTokens bool
	// PasswordHash is the cost of passwords the security API sets; the zero
	// value is production's. Only Go code can choose another: tests use a
	// tiny cost.
	PasswordHash auth.HashParams
	// Redirect tunes downloadRedirect and cdnRedirect (decision 0029).
	Redirect Redirect
	// FolderDownloadMaxConcurrent is how many folder archive downloads run
	// at once (download.folder.max_concurrent_requests); zero means
	// Artifactory's 10.
	FolderDownloadMaxConcurrent int
	// AQL bounds AQL searches; the zero value has no cap. Its MaxResults
	// caps the GET searches too.
	AQL AQLLimits
	// Search limits what the GET searches return; the zero value limits
	// nothing.
	Search SearchLimits
	// DockerSubdomainMatch turns on the Docker subdomain method
	// (docker.subdomain_match): <repo>.<BaseURL host>/v2/... addresses repo.
	DockerSubdomainMatch bool
	// AllowDuplicateBuildEntries accepts a build-info publish whose module
	// lists one file twice (build.block_duplicate_entries false); the zero
	// value refuses it, as Artifactory 7.x does by default.
	AllowDuplicateBuildEntries bool
	// EnforceIdenticalChecksumDeploy makes a checksum deploy over a file
	// with the same content need the delete permission
	// (deploy.enforce_permission_check_on_identical_checksum_deploy); the
	// zero value lets it through with deploy alone, as Artifactory 7.x does
	// by default.
	EnforceIdenticalChecksumDeploy bool
	// NPMMaxPublishSize caps an npm publish request (npm.max_publish_size);
	// zero means decision 0034's default.
	NPMMaxPublishSize int64
	// GlobalEnvironmentLimit caps the global environments, DEV and PROD
	// included (environments.global_limit); zero means
	// roles.DefaultGlobalEnvironmentLimit.
	GlobalEnvironmentLimit int
	// Work is the runtime's work queue, which formats ask for derived work
	// (decision 0033). Nil writes that work to the store's queue directly,
	// where the workers find it on their next poll.
	Work *work.Queue
	// Formats returns a package type's format; nil means formats.For. Only
	// tests set it, to substitute formats that record their hooks.
	Formats func(repo.PackageType) format.Handler
	// Protocols are mounted through the router kit after the built-in ones
	// (protocols). Only tests set it, to mount a test-only protocol.
	Protocols []pkgroute.Protocol
}

// protocols are the package protocols with routers of their own, mounted
// through the router kit (decision 0032). A format with a protocol adds its
// constructor here.
var protocols = []func(Config) pkgroute.Protocol{
	func(Config) pkgroute.Protocol { return golang.Protocol() },
	func(cfg Config) pkgroute.Protocol {
		return npm.Protocol(npm.Options{MaxPublishSize: cfg.NPMMaxPublishSize})
	},
}

// newRegistry is the Docker registry for cfg. A nil token service must not
// become a non-nil interface, which would leave the token endpoint
// registered with nothing to sign.
func newRegistry(cfg Config, artSvc artifactoryService) *docker.Registry {
	o := docker.Options{
		Store:          cfg.Store,
		Blobs:          cfg.Blobs,
		SubdomainMatch: cfg.DockerSubdomainMatch,
		BaseURL:        cfg.BaseURL,
		Fault:          func(r *http.Request, err error) { noteCause(r.Context(), err) },
		// A layer downloads as any file does: redirected to a signed URL
		// when the repository says so (decision 0029), and counted.
		ServeBlob: func(w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item) {
			p, _ := auth.FromContext(r.Context())
			artSvc.serveFile(w, r, cfg.Store, found{repo: c, item: it}, p)
		},
		// A manifest at or above the minimum size redirects too, as on 7.x
		// (RTFACT-29705), carrying its media type to the object store.
		RedirectManifest: func(
			w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item, mediaType string,
		) bool {
			return artSvc.redirectManifest(w, r, cfg.Store, found{repo: c, item: it}, mediaType)
		},
	}
	if cfg.Tokens != nil {
		o.Tokens = cfg.Tokens
	}
	return docker.New(o)
}

// Mirrors is what the storage endpoints need from the runtime's blob
// mirrors (#18).
type Mirrors interface {
	// Optimize asks every mirror for an immediate repair pass.
	Optimize()
	// Status returns each mirror's status.
	Status(ctx context.Context) ([]MirrorStatus, error)
}

// MirrorStatus is one tier's mirror: "blob" or "cold_blob".
type MirrorStatus struct {
	Tier string
	mirror.Status
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
	artSvc := artifactoryService{
		cfg:      cfg,
		signWarn: newThrottle(signWarnEvery),
		folderSlots: make(
			chan struct{},
			cmp.Or(cfg.FolderDownloadMaxConcurrent, defaultFolderSlots),
		),
		instanceID: new(atomic.Pointer[string]),
	}
	// The registry exists before the middleware, whose challenge it writes.
	registry := newRegistry(cfg, artSvc)
	r.Use(
		requestID,
		recoverPanic(logger),
		accessLog(logger),
		baseurl.Middleware(cfg.BaseURL),
		docker.APIVersion,
		registry.Challenge,
		normalizeContentType,
		collectionSlash,
		plusAsSpace,
	)
	mounted := make([]pkgroute.Protocol, 0, len(protocols)+len(cfg.Protocols))
	for _, p := range protocols {
		mounted = append(mounted, p(cfg))
	}
	mounted = append(mounted, cfg.Protocols...)
	if cfg.Auth != nil {
		r.Use(authenticate(cfg.Auth, cfg.Store, func(escaped string) bool {
			return slices.ContainsFunc(mounted, func(p pkgroute.Protocol) bool {
				return p.IsAbsent(escaped)
			})
		}))
	}
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)

	// One verifier per handler, so providers' keys stay cached between
	// exchanges.
	accSvc := accessService{cfg: cfg, idTokens: idtoken.New(idtoken.Options{})}

	// Hand-written routes go first so they win over anything a generated mux
	// might later claim for the same prefix.
	r.Get("/artifactory/api/system/ping", artSvc.ping(artifactoryService.probeArtifactory))
	r.With(bodyLimit(sinkBodyLimit)).Post("/artifactory/api/system/usage", sinkHandler)
	r.With(bodyLimit(sinkBodyLimit)).Post("/jfconnect/api/v1/backoffice/metrics/log", sinkHandler)
	r.Get("/access/api/v1/system/ping", artSvc.ping(artifactoryService.probeAccess))
	// Access's and the router's own probes, which JFrog's Helm charts and
	// secret rotator poll through the public URL. The router is ready when
	// the services behind it are.
	r.Get("/access/api/v1/system/readiness", artSvc.readiness(artifactoryService.probeAccess))
	r.Get("/access/api/v1/system/liveness", liveness)
	r.Get("/router/api/v1/system/readiness", artSvc.readiness(artifactoryService.probeArtifactory))
	r.Get("/router/api/v1/system/liveness", liveness)
	r.Get("/router/api/v1/system/health", artSvc.routerHealth)
	r.With(bodyLimit(managementBodyLimit)).
		Patch("/artifactory/api/system/configuration", artSvc.patchConfiguration)
	artSvc.registerItemRoutes(r)
	artSvc.registerStorageRoutes(r)
	artSvc.registerTrashRoutes(r)
	artSvc.registerMultipartRoutes(r)
	artSvc.registerTokenRoutes(r)
	artSvc.registerSearchRoutes(r)
	registry.Register(r)
	for _, p := range mounted {
		pkgroute.Mount(r, artSvc.protocolOptions(), p)
	}

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
