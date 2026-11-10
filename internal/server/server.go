// Package server builds the single chi router that serves every JFrog path
// prefix: /artifactory, /access, /event, /router, /jfconnect and /v2. Flat
// CRUD families come from the generated strict servers; item paths and
// protocol handlers are hand-written and mounted first (decision 0002).
package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	accessmodule "github.com/weaveplatform/artifactweave/migration/access"
	accesshttp "github.com/weaveplatform/artifactweave/migration/access/adapters/http"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	artifactshttp "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/smart"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/usage"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	alpinehttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/alpine/http"
	cranhttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/cran/http"
	debianhttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/debian/http"
	dockerhttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker/http"
	luarockshttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/luarocks/http"
	opkghttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/opkg/http"
	p2http "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/p2/http"
	rpmhttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/rpm/http"
	terraformbackendhttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/terraformbackend/http"
	vcshttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/vcs/http"
	configurationhttp "github.com/weaveplatform/artifactweave/migration/configuration/adapters/http"
	configapi "github.com/weaveplatform/artifactweave/migration/configuration/api"
	"github.com/weaveplatform/artifactweave/migration/configuration/core/descriptor"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	accessgen "github.com/weaveplatform/artifactweave/migration/platform/gen/access"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/migration/platform/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/migration/platform/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/migration/platform/gen/grid"
	pfedgen "github.com/weaveplatform/artifactweave/migration/platform/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/migration/platform/gen/worker"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Config is what the router needs from the application configuration.
type Config struct {
	// RequestLimits owns shared request rates and this process's concurrency
	// permits. Nil disables limits for embedders; the application wires them.
	RequestLimits *apilimit.Service
	// RemoteHTTP owns repository-scoped connection pools, of which the
	// router needs only Remove, for a deleted repository. The application
	// closes it after draining requests; source adapters resolve credentials
	// per revision. Assign it only a pool that exists, or leave it nil: a nil
	// *upstream.Pool stored in it is a non-nil interface, so a delete would
	// call Remove on the nil pool, which panics on the pool's lock.
	RemoteHTTP interface{ Remove(key string) }
	// Keys derives the key for short-lived Terraform download URLs.
	Keys *crypt.Keyring
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
	// RemoteSources is the direct remote read boundary. Nil preserves cache-only
	// behavior; production activation belongs to the adapter tickets. Native
	// protocols opt in separately through their pkgroute.Source map.
	RemoteSources *source.Service
	// RegistrySources opts Docker, OCI and OCI Helm into authenticated remote reads.
	RegistrySources *source.Service
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
	Auth *authn.Authenticator
	// Mirrors administers the blob mirrors; nil when no tier is a mirror.
	Mirrors artifactshttp.Mirrors
	// RemoteStatus serves artifactweave-only, administrator-only diagnostics.
	RemoteStatus func(context.Context, string, string) (smart.SmartStatus, error)
	// Multipart serves the JFrog CLI's multipart upload protocol; nil
	// reports it unsupported, so the CLI uploads in one request.
	Multipart *multipart.Service
	// Tokens issues, refreshes and revokes access tokens for the token API;
	// nil answers that API 501.
	Tokens *token.Service
	// Access is the access module's API, which app builds over Store, Auth
	// and Tokens. When every contract in it is nil and Store is set, New
	// binds one over those fields, with AllowBasicAuthTokens and
	// GlobalEnvironmentLimit as its settings, so a Config built by hand, as
	// tests build it, answers as the application's does. Without a store it
	// stays zero.
	Access accessapi.API
	// Configuration is the configuration module's API, through which PATCH
	// /api/system/configuration applies the descriptor and the item routes
	// read its minimum redirect size. When both its contracts are nil and
	// Store is set, New binds one over Store and Access with the descriptor
	// core, as it binds Access. Without a store it stays zero.
	Configuration configapi.API
	// AllowBasicAuthTokens lets a password sent with Basic create tokens
	// through POST /access/api/v1/tokens (token.allow_basic_auth); the zero
	// value refuses it, as Artifactory 7.x does by default.
	AllowBasicAuthTokens bool
	// PasswordHash is the cost of passwords the security API sets; the zero
	// value is production's. Only Go code can choose another: tests use a
	// tiny cost.
	PasswordHash authn.HashParams
	// Redirect tunes downloadRedirect and cdnRedirect (decision 0029).
	Redirect itemkit.Redirect
	// FolderDownloadMaxConcurrent is how many folder archive downloads run
	// at once (download.folder.max_concurrent_requests); zero means
	// Artifactory's 10.
	FolderDownloadMaxConcurrent int
	// AQL bounds AQL searches; the zero value has no cap. Its MaxResults
	// caps the GET searches too.
	AQL artifactshttp.AQLLimits
	// Search limits what the GET searches return; the zero value limits
	// nothing.
	Search artifactshttp.SearchLimits
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
	// NPMLoginTokenExpiry is how long a token from npm login lasts
	// (npm.login_token_expiry); zero means decision 0034's default.
	NPMLoginTokenExpiry time.Duration
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
	// Formats returns a package type's format; nil means
	// FormatSettings.For. Only tests set it, to substitute formats that
	// record their hooks.
	Formats format.Registry
	// FormatSettings are the formats' server-wide options, such as
	// npm.tag_latest_by_publish; the zero value is each format's default.
	FormatSettings ecosystem.Settings
	// CranArchiveMoverEnabled enables the administrator-only legacy archive
	// migration API. Its zero value leaves migration disabled.
	CranArchiveMoverEnabled bool
	HuggingFaceXetEnabled   bool
	// Protocols are mounted through the router kit after the built-in ones
	// (artifactshttp.Protocols). Only tests set it, to mount a test-only
	// protocol.
	Protocols []pkgroute.Protocol
}

// newRegistry is the Docker registry for cfg, with the access module's token
// service and user accounts. A nil token service must not become a non-nil
// interface, which would leave the token endpoint registered with nothing to
// sign: cfg.Access holds nil interfaces, never typed nils, and a nil
// interface converts to a nil one, so they are assigned as they are.
func newRegistry(cfg Config, kit *itemkit.Kit) *dockerhttp.Registry {
	o := dockerhttp.Options{
		Sources:        cfg.RegistrySources,
		Store:          cfg.Store,
		Blobs:          cfg.Blobs,
		SubdomainMatch: cfg.DockerSubdomainMatch,
		BaseURL:        cfg.BaseURL,
		// The middleware authenticates every request whenever an
		// authenticator is configured (accesshttp.Authenticate). cfg.Access
		// holds one bound over that authenticator whenever there is a store;
		// without one the registry serves no repository, so the setting
		// decides nothing.
		Authenticated: cfg.Access.Authenticator != nil,
		Fault:         func(r *http.Request, err error) { httpkit.NoteCause(r.Context(), err) },
		// The registry serves only docker, oci and helmoci repositories,
		// whose format takes no settings.
		Formats: ecosystem.For,
		// A layer downloads as any file does: redirected to a signed URL
		// when the repository says so (decision 0029), and counted.
		ServeBlob: func(w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item) {
			p, _ := kernel.FromContext(r.Context())
			kit.ServeFile(w, r, cfg.Store, itemkit.Found{Repo: c, Item: it}, p)
		},
		// A manifest at or above the minimum size redirects too, as on 7.x
		// (RTFACT-29705), carrying its media type to the object store.
		RedirectManifest: func(
			w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item, mediaType string,
		) bool {
			return kit.RedirectManifest(
				w,
				r,
				cfg.Store,
				itemkit.Found{Repo: c, Item: it},
				mediaType,
			)
		},
		Tokens:   cfg.Access.Tokens,
		Identity: cfg.Access.Identity,
	}
	return dockerhttp.New(o)
}

// withAccess returns cfg with Access bound over its store and services by
// the access module when cfg sets a store and no contract of Access (see
// Config.Access).
func withAccess(cfg Config) Config {
	if cfg.Store == nil || !accessUnset(cfg.Access) {
		return cfg
	}
	// New refuses only a nil store, which the check above excludes, so
	// there is no error for New's callers to see.
	if m, err := accessmodule.New(accessmodule.Deps{
		Store: cfg.Store, Authenticator: cfg.Auth, Tokens: cfg.Tokens,
		Settings: accessapi.Settings{
			AllowBasicAuthTokens:   cfg.AllowBasicAuthTokens,
			GlobalEnvironmentLimit: cfg.GlobalEnvironmentLimit,
		},
		Log: cfg.Logger,
	}); err == nil {
		cfg.Access = m.API()
	}
	return cfg
}

// accessUnset reports whether every contract of a is nil. It tests them one
// by one because comparing the API with == panics when a contract holds a
// value that cannot be compared, such as a struct with a map or function
// field. Settings does not count: its zero value is a valid choice. A
// contract added to the API must be added here too; TestUnitWithAccess fails
// until it is.
func accessUnset(a accessapi.API) bool {
	return a.Authenticator == nil && a.Authorizer == nil && a.Tokens == nil &&
		a.Keys == nil && a.Identity == nil && a.Restrictions == nil &&
		a.Projects == nil && a.Grants == nil && a.AnonymousAccess == nil
}

// withConfiguration returns cfg with Configuration bound over its store by
// the descriptor core when cfg sets a store and neither contract of
// Configuration. It runs after withAccess: the descriptor checks the key
// pairs and environments that repository entries name, and writes the
// anonymous access setting, through cfg.Access.
func withConfiguration(cfg Config) Config {
	if cfg.Store == nil || cfg.Configuration.Descriptor != nil ||
		cfg.Configuration.System != nil {
		return cfg
	}
	cfg.Configuration = descriptor.NewAPI(descriptor.Deps{
		Tx:              cfg.Store,
		Repositories:    repos.NewRepositories(cfg.Store, cfg.Access.Keys, cfg.Access.Projects),
		Layouts:         layout.Checker{},
		Configs:         cfg.Store.Configs(),
		AnonymousAccess: cfg.Access.AnonymousAccess,
	})
	return cfg
}

// overlays are the format routes an item GET tries before the generic
// lookup, in their dispatch order (itemkit.Deps.Overlays).
var overlays = []itemkit.Overlay{
	tryAgentMarketplaceItem,
	alpinehttp.TryAlpineItem,
	artifactshttp.TryCondaItem,
	artifactshttp.TryMachineLearningItem,
	artifactshttp.TryChefItem,
	cranhttp.TryCranItem,
	debianhttp.TryDebianItem,
	rpmhttp.TryRPMItem,
	opkghttp.TryOpkgItem,
	p2http.TryP2Item,
}

// kitDeps returns the item kit's dependencies from cfg, with the router's
// overlays and the artifacts handlers' private predicate. The descriptor's
// minimum redirect size comes from the configuration API's System: the
// artifacts module may not import the configuration module's core.
func kitDeps(cfg Config) itemkit.Deps {
	d := itemkit.Deps{
		Store:                          cfg.Store,
		Blobs:                          cfg.Blobs,
		Keys:                           cfg.Keys,
		Redirect:                       cfg.Redirect,
		RemoteSources:                  cfg.RemoteSources,
		RegistrySources:                cfg.RegistrySources,
		Work:                           cfg.Work,
		Formats:                        cfg.Formats,
		FormatSettings:                 cfg.FormatSettings,
		Access:                         cfg.Access,
		EnforceIdenticalChecksumDeploy: cfg.EnforceIdenticalChecksumDeploy,
		Logger:                         cfg.Logger,
		Overlays:                       overlays,
		Private:                        artifactshttp.PrivateItem,
	}
	if cfg.Configuration.System != nil {
		d.DownloadRedirectMinSize = cfg.Configuration.System.DownloadRedirectMinSize
	}
	return d
}

// newKit returns the item kit over cfg (kitDeps), for the root's handlers
// that need one without the artifacts partial.
func newKit(cfg Config) *itemkit.Kit { return itemkit.New(kitDeps(cfg)) }

// artifactsDeps returns the artifacts handlers' dependencies from cfg.
func artifactsDeps(cfg Config) artifactshttp.Deps {
	return artifactshttp.Deps{
		Deps:                        kitDeps(cfg),
		RemoteHTTP:                  cfg.RemoteHTTP,
		RemoteStatus:                cfg.RemoteStatus,
		GC:                          cfg.GC,
		Multipart:                   cfg.Multipart,
		Mirrors:                     cfg.Mirrors,
		BlobLocation:                cfg.BlobLocation,
		AQL:                         cfg.AQL,
		Search:                      cfg.Search,
		AllowDuplicateBuildEntries:  cfg.AllowDuplicateBuildEntries,
		FolderDownloadMaxConcurrent: cfg.FolderDownloadMaxConcurrent,
		HuggingFaceXetEnabled:       cfg.HuggingFaceXetEnabled,
		NPMLoginTokenExpiry:         cfg.NPMLoginTokenExpiry,
		NPMMaxPublishSize:           cfg.NPMMaxPublishSize,
	}
}

// newArtifactoryAPI returns the artifacts partial over cfg, whose item kit
// New hands to the ecosystem partials as well.
func newArtifactoryAPI(cfg Config) artifactshttp.ArtifactoryAPI {
	return artifactshttp.New(artifactsDeps(cfg))
}

// New returns the fully wired handler.
func New(cfg Config) http.Handler {
	cfg = withConfiguration(withAccess(cfg))
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	r := chi.NewRouter()
	artAPI := newArtifactoryAPI(cfg)
	// Every partial embeds the kit newArtifactoryAPI built, so the routes of
	// the whole spec share one set of overlays and one redirect throttle.
	kit := artAPI.Kit
	vcsOps := vcshttp.VCSAPI{Kit: kit}
	npmOps := npmAPI{kit}
	dockerOps := dockerAPI{kit}
	debianOps := debianhttp.DebianAPI{Kit: kit}
	cranOps := cranhttp.CranAPI{Kit: kit, ArchiveMoverEnabled: cfg.CranArchiveMoverEnabled}
	rpmOps := rpmhttp.RPMAPI{Kit: kit}
	sysAPI := configurationhttp.New(configurationhttp.Deps{
		Store:         cfg.Store,
		Blobs:         cfg.Blobs,
		CompatVersion: cfg.CompatVersion,
		Revision:      cfg.Revision,
		BaseURL:       cfg.BaseURL,
		Settings:      configapi.Settings{DockerSubdomainMatch: cfg.DockerSubdomainMatch},
		Configuration: cfg.Configuration,
		Logger:        cfg.Logger,
	})
	accessHTTP := accesshttp.New(accesshttp.Deps{
		API:           cfg.Access,
		Store:         cfg.Store,
		Authenticator: cfg.Auth,
		Tokens:        cfg.Tokens,
		Settings: accessapi.Settings{
			AllowBasicAuthTokens:   cfg.AllowBasicAuthTokens,
			GlobalEnvironmentLimit: cfg.GlobalEnvironmentLimit,
		},
		PasswordHash:  cfg.PasswordHash,
		CompatVersion: cfg.CompatVersion,
		Log:           cfg.Logger,
		Repositories:  repos.NewRepositories(cfg.Store, cfg.Access.Keys, cfg.Access.Projects),
	})
	// The artifacts repositories collection, and access's key pairs.
	slashes := artifactshttp.SlashCollections()
	maps.Copy(slashes, accesshttp.SlashCollections())
	// The registry exists before the middleware, whose challenge it writes.
	registry := newRegistry(cfg, kit)
	r.Use(
		httpkit.ErrorProfiles(errorProfileRules),
		middleware.RequestID,
		middleware.RecoverPanic(logger, redactLoggedPath),
		middleware.AccessLog(logger, redactLoggedPath),
		baseurl.Middleware(cfg.BaseURL),
		smart.SmartTrace,
		dockerhttp.APIVersion,
		registry.Challenge,
		middleware.NormalizeContentType(keepContentType),
		middleware.CollectionSlash(slashes),
		accesshttp.PlusAsSpace,
		artifactshttp.IdempotencyRoutes,
		accesshttp.APIRecoveryContext,
	)
	limits := newLimitOptions(cfg.RequestLimits)
	limiter := requestLimiter{cfg: cfg}
	if cfg.RequestLimits != nil {
		r.Use(limiter.limitIngress(limits))
	}
	mounted := append(artifactshttp.Protocols(artifactsDeps(cfg)), cfg.Protocols...)
	if cfg.Auth != nil {
		r.Use(
			accesshttp.Authenticate(
				newAuthnOptions(cfg.Auth, cfg.Store, func(req *http.Request) bool {
					if terraformbackendhttp.IsPath(req.URL.Path) {
						return terraformbackendhttp.SkipsAuthentication(req)
					}
					return slices.ContainsFunc(mounted, func(p pkgroute.Protocol) bool {
						return p.SkipsAuthentication(req)
					})
				}),
			),
		)
	}
	// After authentication, which it needs, and ahead of routing, which
	// cannot match it (HelmPartialReindex).
	if cfg.Store != nil {
		r.Use(accessHTTP.CheckAPIRestriction(configurationhttp.HealthRequest, writeLimitError))
	}
	if cfg.RequestLimits != nil {
		r.Use(limiter.limitPrincipal(limits))
	}
	r.Use(artAPI.HelmPartialReindex)
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)

	// Hand-written routes go first so they win over anything a generated mux
	// might later claim for the same prefix.
	r.Get("/artifactory/api/system/ping", sysAPI.ArtifactoryPing())
	r.With(middleware.BodyLimit(configurationhttp.SinkBodyLimit)).
		Post("/artifactory/api/system/usage", configurationhttp.SinkHandler)
	r.With(middleware.BodyLimit(configurationhttp.SinkBodyLimit)).
		Post("/jfconnect/api/v1/backoffice/metrics/log", configurationhttp.SinkHandler)
	r.Get("/access/api/v1/system/ping", sysAPI.AccessPing())
	// Access's and the router's own probes, which JFrog's Helm charts and
	// secret rotator poll through the public URL. The router is ready when
	// the services behind it are.
	r.Get("/access/api/v1/system/readiness", sysAPI.AccessReadiness())
	r.Get("/access/api/v1/system/liveness", configurationhttp.Liveness)
	r.Get("/router/api/v1/system/readiness", sysAPI.RouterReadiness())
	r.Get("/router/api/v1/system/liveness", configurationhttp.Liveness)
	r.Get("/router/api/v1/system/health", sysAPI.RouterHealth)
	r.Get("/artifactory/api/artifactweave/remote-status/{repoKey}", artAPI.SmartStatus)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).
		Patch("/artifactory/api/system/configuration", sysAPI.PatchConfiguration)
	// The item routes and the handwritten routes beside them keep their
	// original order (item paths, AQL, agent marketplace, CRAN uploads,
	// transfer, Debian snapshots), and all run before
	// registerHandwrittenStubs: each claims the pattern the spec derives,
	// so it replaces the 501 stub registered under that pattern rather than
	// sitting beside it (decision 0002).
	artAPI.RegisterItemRoutes(r)
	agentPluginItems{kit}.registerAgentMarketplaceRoutes(r)
	cranOps.RegisterCranRoutes(r)
	artAPI.RegisterTransferRoutes(r)
	debianOps.RegisterDebianSnapshotRoutes(r)
	artAPI.RegisterStorageRoutes(r)
	artAPI.RegisterTrashRoutes(r)
	artAPI.RegisterMultipartRoutes(r)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).Group(accessHTTP.RegisterRoutes)
	artAPI.RegisterSearchRoutes(r)
	mavenAPI{kit}.registerMavenRoutes(r)
	// GitLab subgroup owners span path segments; the generated VCS routes
	// handle the ordinary one-segment forms.
	r.Get("/artifactory/api/vcs/*", vcsOps.VCSNested)
	registry.Register(r)
	terraformbackendhttp.Register(
		r,
		terraformbackendhttp.Options{
			Items:        artAPI.Items(cfg.Store),
			Keys:         cfg.Keys,
			Fault:        func(r *http.Request, err error) { httpkit.NoteCause(r.Context(), err) },
			Authorizer:   cfg.Access.Authorizer,
			Identity:     cfg.Access.Identity,
			Restrictions: cfg.Access.Restrictions,
		},
	)
	for _, p := range mounted {
		pkgroute.Mount(r, artAPI.ProtocolOptions(), p)
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
		g.Use(
			middleware.BodyLimit(middleware.ManagementBodyLimit),
			requestDetails(newDetailOptions()),
		)
		strictOpts := artgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}
		artgen.HandlerWithOptions(
			artgen.NewStrictHandlerWithOptions(artifactoryServer{
				SecurityAPI:    accessHTTP.Security,
				SystemAPI:      sysAPI,
				ArtifactoryAPI: &artAPI,
				VCSAPI:         &vcsOps,
				npmAPI:         &npmOps,
				dockerAPI:      &dockerOps,
				DebianAPI:      &debianOps,
				CranAPI:        &cranOps,
				RPMAPI:         &rpmOps,
			}, nil, strictOpts),
			artgen.ChiServerOptions{
				BaseURL:          "/artifactory",
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
		accessOpts := accessgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}
		accessgen.HandlerWithOptions(
			accessgen.NewStrictHandlerWithOptions(
				accessServer{AccessService: accessHTTP.Access},
				nil,
				accessOpts,
			),
			accessgen.ChiServerOptions{
				BaseURL:          "/access",
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
		eventOpts := eventgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}
		eventgen.HandlerWithOptions(
			eventgen.NewStrictHandlerWithOptions(eventService{}, nil, eventOpts),
			eventgen.ChiServerOptions{
				BaseURL:          "/event",
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
		distributionOpts := distributiongen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
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
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
		workerOpts := workergen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}
		workergen.HandlerWithOptions(
			workergen.NewStrictHandlerWithOptions(workerService{}, nil, workerOpts),
			workergen.ChiServerOptions{
				BaseURL:          "/worker",
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
		pfedOpts := pfedgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}
		pfedgen.HandlerWithOptions(
			pfedgen.NewStrictHandlerWithOptions(pfedService{}, nil, pfedOpts),
			pfedgen.ChiServerOptions{
				BaseURL:          "/pfed",
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
		gridOpts := gridgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}
		gridgen.HandlerWithOptions(
			gridgen.NewStrictHandlerWithOptions(gridService{}, nil, gridOpts),
			gridgen.ChiServerOptions{
				BaseURL:          "/grid",
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
	})

	pypiAPI{kit}.registerPypiMetadataRoutes(r, artAPI.ProtocolOptions())
	puppetAPI{kit}.registerPuppetMetadataRoutes(r, artAPI.ProtocolOptions())
	chefAPI{kit}.registerChefReindex(r)

	// Last, so every real handler above has already claimed its pattern.
	registerHandwrittenStubs(r, logger)

	return r
}

// keepContentType reports the requests that keep their vendor media type
// (middleware.NormalizeContentType): registry requests, whose manifest
// media type is part of the content they store and serve back, and Git LFS
// protocol requests, whose media type is the protocol's.
func keepContentType(r *http.Request) bool {
	return dockerhttp.IsRegistryPath(r.URL.Path) ||
		strings.HasPrefix(r.URL.Path, "/artifactory/api/lfs/")
}

// redactLoggedPath is a path as the access and panic logs record it, with
// the credentials that LuaRocks's upload paths and npm's logout paths name
// replaced. Each redactor leaves a path outside its protocol unchanged.
func redactLoggedPath(path string) string {
	return npmLoggedPath(luarockshttp.RedactAPIKey(path))
}

// newAuthnOptions returns Authenticate's options as the router composes
// them: the multipart calls verify themselves, npm's login and logout are
// skipped before the skips the caller adds, the registry decides where a
// registry token is good and which paths challenge anonymous requests,
// npm's configuration operations keep their credential, and Access's and
// the system's public routes answer anyone.
func newAuthnOptions(
	a *authn.Authenticator,
	st store.Store,
	skip ...func(*http.Request) bool,
) accesshttp.AuthnOptions {
	public := accesshttp.PublicRoutes()
	maps.Copy(public, configurationhttp.PublicRoutes())
	return accesshttp.AuthnOptions{
		Auth:               a,
		Store:              st,
		SelfAuthenticated:  artifactshttp.SelfAuthenticated(),
		Skip:               append([]func(*http.Request) bool{npmUserRequest}, skip...),
		RegistryPath:       dockerhttp.IsRegistryPath,
		ChallengeAnonymous: dockerhttp.ChallengesAnonymous,
		CredentialKeepers: []func(context.Context, *http.Request, kernel.Principal) context.Context{
			keepNPMCredential,
		},
		PublicRoutes: public,
	}
}

// newLimitOptions returns the request-limit middleware's options as the
// router composes them: the probes (configurationhttp.HealthRequest) and
// the recovery API (APIRecoveryRequest) pass the shared limits.
func newLimitOptions(limits *apilimit.Service) limitOptions {
	return limitOptions{
		Health:   configurationhttp.HealthRequest,
		Recovery: accesshttp.APIRecoveryRequest,
		Limits:   limits,
	}
}

// detailOptions name the requests whose details requestDetails keeps. Each
// contribution is declared beside the handler that reads what it keeps.
type detailOptions struct {
	// SearchQuery reports the requests whose raw query is kept
	// (httpkit.RawSearchQuery), for parameters the generated binding drops.
	SearchQuery []func(*http.Request) bool
	// Rewrite normalises a request's body before the generated binding
	// decodes it, each in order; an error answers 400 as a binding error.
	Rewrite []func(*http.Request) error
	// RawBody reports the requests whose raw body is kept (httpkit.RawBody),
	// for handlers that must tell what the decoded type cannot.
	RawBody []func(*http.Request) bool
}

// requestDetails keeps what the strict handlers need beyond their decoded
// request: the Accept header of every request, for media-type negotiation,
// and the raw query, rewritten body and raw body of the requests o names.
// It runs after middleware.BodyLimit, so the copy is bounded.
func requestDetails(o detailOptions) func(http.Handler) http.Handler {
	matches := func(preds []func(*http.Request) bool, r *http.Request) bool {
		return slices.ContainsFunc(preds, func(p func(*http.Request) bool) bool { return p(r) })
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := httpkit.WithAccept(r.Context(), r.Header.Get("Accept"))
			if matches(o.SearchQuery, r) {
				ctx = httpkit.WithSearchQuery(ctx, r.URL.RawQuery)
			}
			for _, rewrite := range o.Rewrite {
				if err := rewrite(r); err != nil {
					middleware.BindingError(http.StatusBadRequest)(w, r, err)
					return
				}
			}
			if matches(o.RawBody, r) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					middleware.BindingError(http.StatusBadRequest)(w, r, err)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(b))
				ctx = httpkit.WithRawBody(ctx, b)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// newDetailOptions returns requestDetails' options as the router composes
// them, from the contributions beside each handler, access's through
// DetailRules: the property and GAVC searches' raw queries, the policy body
// rewrite, and the raw bodies of repository updates, build-info publishes
// and appends, permission creates and group updates.
func newDetailOptions() detailOptions {
	access := accesshttp.DetailRules()
	return detailOptions{
		SearchQuery: []func(*http.Request) bool{artifactshttp.KeepsSearchQuery},
		Rewrite:     access.Rewrite,
		RawBody: append(
			[]func(*http.Request) bool{
				artifactshttp.KeepsRepositoryBody,
				artifactshttp.KeepsBuildBody,
			},
			access.RawBody...,
		),
	}
}
