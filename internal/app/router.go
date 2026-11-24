package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/api"
	accessmodule "github.com/weaveplatform/artifactweave/internal/access"
	accesshttp "github.com/weaveplatform/artifactweave/internal/access/adapters/http"
	accessldap "github.com/weaveplatform/artifactweave/internal/access/adapters/ldap"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts"
	artifactshttp "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/gc"
	itemcore "github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/smart"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/usage"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	agentpluginshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentplugins/http"
	alpinehttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine/http"
	chefhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef/http"
	cranhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran/http"
	debianhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian/http"
	dockerhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker/http"
	helmhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm/http"
	luarockshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/luarocks/http"
	mavenhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven/http"
	npmhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm/http"
	opkghttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg/http"
	p2http "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/p2/http"
	puppethttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet/http"
	pypihttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi/http"
	rpmhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm/http"
	terraformhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/http"
	terraformbackendhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraformbackend/http"
	vcshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/vcs/http"
	"github.com/weaveplatform/artifactweave/internal/configuration"
	configurationhttp "github.com/weaveplatform/artifactweave/internal/configuration/adapters/http"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/federation"
	federationhttp "github.com/weaveplatform/artifactweave/internal/federation/adapters/http"
	federationpeer "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications"
	notificationshttp "github.com/weaveplatform/artifactweave/internal/notifications/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/internal/platform/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/internal/platform/gen/grid"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	pfedgen "github.com/weaveplatform/artifactweave/internal/platform/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/internal/platform/gen/worker"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention"
	retentionhttp "github.com/weaveplatform/artifactweave/internal/retention/adapters/http"
)

// RouterStore is the metadata store NewRouter hands to the handlers,
// middleware and bindings it builds: the union of the store types they
// declare, so the router asks for no accessor that none of them uses, and a
// handler that starts using one more makes this interface, and nothing
// else, grow. Every call joins the transaction its context carries. The
// composite SQL store is a RouterStore, as is the in-memory composite that
// the tests build routers over.
type RouterStore interface {
	// Configuration's bindings inspect enclosing authority transactions.
	configuration.Store
	retention.Store
	federation.Store
	// The artifacts handlers, with their item kit, package routes and
	// replays.
	artifactshttp.Store
	// The Docker registry's uploads.
	registry.Store
	// The repositories given to the descriptor core and the access
	// handlers, and the request budget's repository lookup.
	repos.Store
	// The access module's bindings.
	accessmodule.Store
	// The access handlers and the authentication middleware.
	accesshttp.Store
	// The system handlers' instance identity.
	instance.Store
	// The receipts of the keyed operations' replay (keyed.go).
	Replays() idempotency.ReplayStore
	// The peer records and replay cache the peer transport verifies with.
	PeerStore
	// Subscription administration and its sealed handler secrets.
	notifications.Store
}

var _ RouterStore = (*appsql.Store)(nil)

// RouterConfig is what NewRouter needs: the stores, the services app opens
// over them and the settings it reads from the application configuration.
// The content routes need Store and Blobs; a nil service turns off what it
// serves, as each field says, and the zero value of a setting is
// Artifactory 7.x's default. The caller owns every store and service here
// and closes them only after the handler has drained.
type RouterConfig struct {
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
	// Logging shares actual level decisions with the owning runtime.
	Logging *logging.Control
	// OnClose registers cleanup for modules built by this router, after the
	// handler drains. Required to bind internally owned logger sessions; nil
	// leaves that API unavailable. Injected Configuration APIs keep their owner.
	// Production supplies the runtime-owned configuration API instead.
	OnClose func(func())
	// RemoteSources is the direct remote read boundary. Nil preserves cache-only
	// behavior; production activation belongs to the adapter tickets. Native
	// protocols opt in separately through their pkgroute.Source map.
	RemoteSources *source.Service
	// RegistrySources opts Docker, OCI and OCI Helm into authenticated remote reads.
	RegistrySources *source.Service
	// Store is the metadata store and Blobs the blob store that the content
	// handlers read and write. ColdBlobs is the archive tier, nil when none
	// is configured.
	Store     RouterStore
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
	// LDAP is the directory client the LDAP connection test asks. A router
	// over Store without one asks a client with the system's trust, as the
	// application's does.
	LDAP ldap.Directory
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
	// and Tokens. When every contract in it is nil and Store is set,
	// NewRouter binds one over those fields, with AllowBasicAuthTokens and
	// GlobalEnvironmentLimit as its settings, so a RouterConfig built by
	// hand, as tests build it, answers as the application's does. Without a
	// store it stays zero.
	Access accessapi.API
	// Notifications is the runtime's subscription administration module. A
	// router built directly over Store constructs one with the same access and
	// repository bindings when this is nil.
	Notifications *notifications.Module
	// Configuration is the configuration module's API, through which PATCH
	// /api/system/configuration applies and GET exports the descriptor; item routes
	// read its minimum redirect size. When its descriptor, export and system contracts are nil and
	// Store is set, NewRouter binds one over Store and Access with the
	// configuration module, as it binds Access. Without a store it stays zero.
	Configuration configapi.API
	// Retention owns package policy administration; a router with only Store
	// binds the same module after Access, as runtime module assembly does.
	Retention *retention.Module
	// Federation owns single-replication configuration. Direct router assembly
	// binds it over Store and Access when nil, matching runtime assembly.
	Federation *federation.Module
	// PeerRemote is the signed peer client pairing calls other instances
	// through (decision 0021); nil refuses a redeem or an unpair with 500.
	PeerRemote pairing.Remote
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
	// VirtualCache is the process's merged virtual metadata cache, sized
	// by virtual.metadata_cache (decision 0008, R8). Nil builds one with
	// the default bounds, so a router built by hand caches as the
	// application's does.
	VirtualCache *virtual.Cache
}

// newRegistry is the Docker registry for cfg, with the access module's token
// service and user accounts. A nil token service must not become a non-nil
// interface, which would leave the token endpoint registered with nothing to
// sign: cfg.Access holds nil interfaces, never typed nils, and a nil
// interface converts to a nil one, so they are assigned as they are.
func newRegistry(cfg RouterConfig, kit *itemkit.Kit) *dockerhttp.Registry {
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
		ServeBlob: func(w http.ResponseWriter, r *http.Request, c repo.Config, it model.Item) {
			p, _ := kernel.FromContext(r.Context())
			kit.ServeFile(w, r, cfg.Store, itemkit.Found{Repo: c, Item: it}, p)
		},
		// A manifest at or above the minimum size redirects too, as on 7.x
		// (RTFACT-29705), carrying its media type to the object store.
		RedirectManifest: func(
			w http.ResponseWriter, r *http.Request, c repo.Config, it model.Item, mediaType string,
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
		CurrentAccess: func(ctx context.Context) (kernel.Principal, accessapi.Decider, error) {
			p, a, err := kit.CurrentAccess(ctx)
			if err != nil {
				return p, nil, fmt.Errorf("refresh registry publication authority: %w", err)
			}
			return p, a.Decider, nil
		},
	}
	return dockerhttp.New(o)
}

// withAccess returns cfg with Access bound over its store and services by
// the access module when cfg sets a store and no contract of Access (see
// RouterConfig.Access).
func withAccess(cfg RouterConfig) RouterConfig {
	if cfg.Store == nil || !accessUnset(cfg.Access) {
		return cfg
	}
	// New refuses only a nil store, which the check above excludes, so
	// there is no error for New's callers to see.
	if m, err := accessmodule.New(accessmodule.Deps{
		Store: cfg.Store, Authenticator: cfg.Auth, Tokens: cfg.Tokens,
		ProjectRepositories: repos.NewProjectRepositoryReader(cfg.Store),
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
		a.Projects == nil && a.Grants == nil && a.AnonymousAccess == nil && a.LDAP == nil &&
		a.LDAPGroups == nil &&
		a.Security == nil
}

// hidesUnauthorized reads the security configuration's
// hideUnauthorizedResources through access, or reports it off, 7.x's
// default, for a router built without a security policy binding.
func hidesUnauthorized(a accessapi.API) httpkit.Hiding {
	if a.Security == nil {
		return func(context.Context) (bool, error) { return false, nil }
	}
	return a.Security.HidesUnauthorized
}

// withConfiguration returns cfg with Configuration bound over its store by
// the configuration module when cfg sets a store and no descriptor, export or
// system contract. It runs after withAccess: the descriptor checks the key
// pairs and environments that repository entries name, and writes the
// anonymous access setting, through cfg.Access.
func withConfiguration(cfg RouterConfig) RouterConfig {
	if cfg.Store == nil || cfg.Configuration.Descriptor != nil || cfg.Configuration.Export != nil ||
		cfg.Configuration.System != nil {
		return cfg
	}
	ownedLogging := cfg.Logging
	if cfg.OnClose == nil || cfg.Configuration.Loggers != nil {
		ownedLogging = nil
	}
	m, err := configuration.New(configuration.Deps{
		Store:        cfg.Store,
		Repositories: repos.NewRepositories(cfg.Store, cfg.Access.Keys, cfg.Access.Projects, repositoryLifecycle(cfg)),
		Layouts:      layout.Checker{}, Access: cfg.Access, Logging: ownedLogging, Log: cfg.Logger,
		StoragePolicies: artifacts.StoragePoliciesOf(cfg.Store),
		Settings:        configapi.Settings{DockerSubdomainMatch: cfg.DockerSubdomainMatch, BaseURL: cfg.BaseURL},
	})
	if err == nil {
		bound := m.API()
		if cfg.Configuration.Loggers != nil {
			bound.Loggers = cfg.Configuration.Loggers
		}
		cfg.Configuration = bound
		if cfg.OnClose != nil {
			cfg.OnClose(m.Close)
		}
	}

	return cfg
}

// configuredBaseURL is the configured base URL each request resolves its
// own against: the configuration module's effective value, a stored urlBase
// over base_url, when the module is bound, and base_url otherwise.
func configuredBaseURL(cfg RouterConfig) func(context.Context) string {
	if ws := cfg.Configuration.WebServer; ws != nil {
		return ws.BaseURL
	}
	return func(context.Context) string { return cfg.BaseURL }
}

// federatedAPIs are the federated repository operations under
// /api/federation and the peer API's configuration receiver (FED-01); each
// keeps its 501 without a federation module over a store that serves them.
func federatedAPIs(cfg RouterConfig) (federationhttp.FederatedAPI, federationpeer.RepositoryServer) {
	var ops federationhttp.FederatedAPI
	var receiver federationpeer.RepositoryServer
	if cfg.Federation == nil {
		return ops, receiver
	}
	if o, err := cfg.Federation.Operations(); err == nil {
		ops.Operations = o
	}
	if r, err := cfg.Federation.Receiver(); err == nil {
		receiver.Receiver = r
	}
	return ops, receiver
}

// memberBase is the base URL a federated repository's own member is read
// back with: federatedRepoUrlBase, urlBase or the configured base URL
// (configdoc.FederatedBase). Without a store there is none.
func memberBase(cfg RouterConfig) func(context.Context) (string, error) {
	if cfg.Store == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		return configdoc.FederatedBase(ctx, cfg.Store.Configs(), cfg.BaseURL) //nolint:wrapcheck // names the setting
	}
}

// withFederation runs after Access and before Configuration: descriptor
// repository deletion must share the same replication lifetime cleanup.
func withFederation(cfg RouterConfig) RouterConfig {
	if cfg.Store == nil || cfg.Federation != nil {
		return cfg
	}
	if m, err := federation.New(federation.Deps{
		UserQueryLimit: cfg.Search.UserQueryLimit,
		Lifetimes:      repos.NewRepositoryLifetimes(cfg.Store),
		Store:          cfg.Store, Access: cfg.Access, Ownership: repos.NewProjectRepositoryLookup(cfg.Store),
		Repositories: repos.NewRepositories(cfg.Store, cfg.Access.Keys, cfg.Access.Projects),
		BaseURL:      cfg.BaseURL,
	}); err == nil {
		// New refuses only a nil store, excluded above, and performs no I/O.
		cfg.Federation = m
	}
	return cfg
}

func repositoryLifecycle(cfg RouterConfig) artifactsapi.RepositoryLifecycle {
	var all repositoryLifecycles
	if cfg.Federation != nil {
		all = append(all, cfg.Federation.RepositoryLifecycle())
	}
	if cfg.Store != nil {
		all = append(all, configuration.RepositoryLifecycle(cfg.Store))
	}
	if len(all) == 0 {
		return nil
	}
	return all
}

// repositoryLifecycles runs each consumer's lifecycle in turn, inside the
// repository change's transaction; the first failure stops it and rolls the
// change and every earlier consumer's effect back.
type repositoryLifecycles []artifactsapi.RepositoryLifecycle

func (l repositoryLifecycles) ResetRepository(ctx context.Context, key string) error {
	for _, lifecycle := range l {
		if err := lifecycle.ResetRepository(ctx, key); err != nil {
			return err //nolint:wrapcheck // the consumer names what failed
		}
	}
	return nil
}

func (l repositoryLifecycles) RefreshRepository(ctx context.Context, key string) error {
	for _, lifecycle := range l {
		if err := lifecycle.RefreshRepository(ctx, key); err != nil {
			return err //nolint:wrapcheck // the consumer names what failed
		}
	}
	return nil
}

// AdmitRepository and CreatedRepository forward to the consumers that take
// part in repository writes (artifactsapi.RepositoryAdmission), so wrapping
// them in the composite does not hide that they do.
func (l repositoryLifecycles) AdmitRepository(ctx context.Context, c *repo.Config) error {
	for _, lifecycle := range l {
		if a, ok := lifecycle.(artifactsapi.RepositoryAdmission); ok {
			if err := a.AdmitRepository(ctx, c); err != nil {
				return err //nolint:wrapcheck // the consumer's refusal is the answer
			}
		}
	}
	return nil
}

func (l repositoryLifecycles) CreatedRepository(ctx context.Context, key string) error {
	for _, lifecycle := range l {
		if a, ok := lifecycle.(artifactsapi.RepositoryAdmission); ok {
			if err := a.CreatedRepository(ctx, key); err != nil {
				return err //nolint:wrapcheck // the consumer names what failed
			}
		}
	}
	return nil
}

// overlays are the format routes an item GET tries before the generic
// lookup, in their dispatch order (itemkit.Deps.Overlays).
var overlays = []itemkit.Overlay{
	agentpluginshttp.TryAgentMarketplaceItem,
	alpinehttp.TryAlpineItem,
	artifactshttp.TryCondaItem,
	artifactshttp.TryMachineLearningItem,
	artifactshttp.TryChefItem,
	cranhttp.TryCranItem,
	debianhttp.TryDebianItem,
	rpmhttp.TryRPMItem,
	opkghttp.TryOpkgItem,
	p2http.TryP2Item,
	mavenhttp.TryMavenMetadataItem,
	helmhttp.TryHelmNamespaceItem,
}

// kitDeps returns the item kit's dependencies from cfg, with the router's
// overlays and the artifacts handlers' private predicate. The descriptor's
// minimum redirect size comes from the configuration API's System: the
// artifacts module may not import the configuration module's core.
func kitDeps(cfg RouterConfig) itemkit.Deps {
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
func newKit(cfg RouterConfig) *itemkit.Kit { return itemkit.New(kitDeps(cfg)) }

// artifactsDeps returns the artifacts handlers' dependencies from cfg.
func artifactsDeps(cfg RouterConfig) artifactshttp.Deps {
	return artifactshttp.Deps{
		Deps:                        kitDeps(cfg),
		Store:                       cfg.Store,
		RepositoryLifecycle:         repositoryLifecycle(cfg),
		MemberBase:                  memberBase(cfg),
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
		VirtualCache:                cfg.VirtualCache,
	}
}

// newArtifactoryAPI returns the artifacts partial over cfg, whose item kit
// NewRouter hands to the ecosystem partials as well.
func newArtifactoryAPI(cfg RouterConfig) artifactshttp.ArtifactoryAPI {
	return artifactshttp.New(artifactsDeps(cfg))
}

func routerLogger(cfg RouterConfig, name string) *slog.Logger {
	if cfg.Logging != nil {
		return cfg.Logging.Logger(name)
	}
	return cfg.Logger.With("logger", name)
}

// NewRouter returns the handler for every JFrog path prefix over cfg:
// /artifactory, /access, /event, /router, /jfconnect and /v2; /distribution,
// /worker, /pfed and /grid, whose specified operations answer 501 until they
// are built; artifactweave's own peer API under /artifactweave/peer/v1, whose
// operations answer 501 likewise; and the products artifactweave does not implement, which answer
// 501 naming the product (Mission Control 404). Flat CRUD families come from
// the generated strict servers, and the item paths, protocols and other
// hand-written routes are mounted ahead of them. When cfg sets a Store but no
// Access or Configuration contract, NewRouter binds them over it first, as
// RouterConfig.Access and RouterConfig.Configuration describe.
func NewRouter(cfg RouterConfig) http.Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Logging == nil && cfg.OnClose != nil {
		cfg.Logging = logging.New(cfg.Logger)
	}
	if cfg.Logging != nil {
		cfg.Logger = cfg.Logging.Logger("artifactweave")
	}
	cfg = withConfiguration(withFederation(withAccess(cfg)))
	if cfg.LDAP == nil && cfg.Store != nil {
		cfg.LDAP = accessldap.Client{}
	}
	if cfg.Notifications == nil && cfg.Store != nil {
		// New refuses only a nil store and performs no I/O.
		cfg.Notifications, _ = notifications.New(notifications.Deps{
			Store: cfg.Store, Access: cfg.Access,
			Repositories: repos.NewRepositories(cfg.Store, cfg.Access.Keys, cfg.Access.Projects),
			ServiceURL:   cfg.BaseURL,
		})
	}
	subscriptions := &notificationshttp.SubscriptionsAPI{}
	if cfg.Notifications != nil {
		subscriptions.Subscriptions = cfg.Notifications.Subscriptions()
	}
	if cfg.Store != nil && cfg.Retention == nil {
		formats := cfg.Formats
		if formats == nil {
			formats = cfg.FormatSettings.For
		}
		// New refuses only a nil store, excluded above.
		if m, err := retention.New(retention.Deps{
			Store: cfg.Store, Access: cfg.Access, Repositories: repos.NewProjectRepositoryLookup(cfg.Store),
			PackageIndex: pkgindex.NewReader(cfg.Store, formats, &itemcore.Service{Store: cfg.Store}),
		}); err == nil {
			cfg.Retention = m
		}
	}
	cleanupOps := retentionhttp.PackagePoliciesAPI{}
	if cfg.Retention != nil {
		policies := cfg.Retention.Policies()
		cleanupOps.Policies = &policies
	}
	replicationOps := federationhttp.ReplicationAPI{}
	if cfg.Federation != nil {
		configurations := cfg.Federation.Configurations()
		replicationOps.Configurations = &configurations
		if status, err := cfg.Federation.Status(); err == nil {
			replicationOps.Status = &status
		}
		if coordinator, err := cfg.Federation.Coordinator(); err == nil {
			replicationOps.Executions = &coordinator
		}
	}
	federatedOps, repoServer := federatedAPIs(cfg)
	serviceTrust, pairServer := pairingAPIs(cfg)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.VirtualCache == nil {
		cfg.VirtualCache = virtual.NewCache(virtual.CacheOptions{Log: logger})
	}
	if cfg.FormatSettings.Helm.IndexChanged == nil && cfg.Store != nil {
		cfg.FormatSettings.Helm.IndexChanged = func(ctx context.Context, key string) {
			if err := cfg.VirtualCache.MemberWritten(ctx, cfg.Store.Repos(), key, "index.yaml", false); err != nil {
				logger.ErrorContext(ctx, "Helm virtual index invalidation failed", "member", key, "error", err)
			}
		}
	}
	r := chi.NewRouter()
	artAPI := newArtifactoryAPI(cfg)
	// Every partial embeds the kit newArtifactoryAPI built, so the routes of
	// the whole spec share one set of overlays and one redirect throttle.
	kit := artAPI.Kit
	vcsOps := vcshttp.VCSAPI{Kit: kit}
	npmOps := npmhttp.NPMAPI{Kit: kit}
	dockerOps := dockerhttp.DockerAPI{Kit: kit}
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
		Logger:        routerLogger(cfg, "artifactweave.configuration"),
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
		Log:           routerLogger(cfg, "artifactweave.access"),
		Repositories:  repos.NewRepositories(cfg.Store, cfg.Access.Keys, cfg.Access.Projects),
		LDAP:          cfg.LDAP,
	})
	// The artifacts repositories collection, and access's key pairs.
	slashes := artifactshttp.SlashCollections()
	maps.Copy(slashes, accesshttp.SlashCollections())
	// The registry exists before the middleware, whose challenge it writes.
	registry := newRegistry(cfg, kit)
	dockerOps.Registry = registry
	head := middleware.HeadAsGet{Serves: headServed(), RunsGet: headRunsGetRoute}
	r.Use(
		httpkit.ErrorProfiles(errorProfileRules),
		middleware.RequestID,
		middleware.RecoverPanic(routerLogger(cfg, "artifactweave.http"), redactLoggedPath),
		middleware.AccessLog(routerLogger(cfg, "artifactweave.http"), redactLoggedPath),
		baseurl.Dynamic(configuredBaseURL(cfg)),
		remote.OriginTrace,
		smart.SmartTrace,
		dockerhttp.APIVersion,
		registry.Challenge,
		middleware.NormalizeContentType(keepContentType),
		middleware.CollectionSlash(slashes),
		accesshttp.PlusAsSpace,
		// After the path rewrites above, whose result it routes, and ahead
		// of everything that reads the method.
		head.Enter,
		keyGate(),
		accesshttp.APIRecoveryContext,
		xzRequests,
		// Every handler's refusal is written through httpkit, which asks the
		// stored setting only for an answer it can hide (#1903).
		httpkit.HideUnauthorized(hidesUnauthorized(cfg.Access)),
		// Marks the REST routes whose anonymous refusal 7.x's exception
		// mappers answer, which the setting hides as they do (#1908).
		httpkit.MapsForbidden(mapperRoutes),
	)
	limits := newLimitOptions(cfg.RequestLimits)
	limiter := requestLimiter{cfg: cfg}
	if cfg.RequestLimits != nil {
		r.Use(limiter.limitIngress(limits))
	}
	mounted := append(artifactshttp.Protocols(artifactsDeps(cfg)), cfg.Protocols...)
	if cfg.Auth != nil {
		r.Use(
			// Ahead of authentication, as 7.x's force-authentication filter
			// is part of its authentication chain: a request without
			// credentials to a forced Maven or P2 virtual repository is
			// challenged whether anonymous access is on or off.
			kit.ForceAuthentication,
			accesshttp.Authenticate(
				newAuthnOptions(cfg.Auth, cfg.Store, cfg.Access.Authorizer, skipsAuthentication(mounted)),
			),
		)
	}
	// Ahead of routing, so nothing under the peer prefix is served without
	// a paired peer's signature; user authentication above skips it.
	r.Use(peerGuard(cfg.Store, time.Now))
	// After authentication, which it needs, and ahead of routing, which
	// cannot match it (HelmPartialReindex).
	if cfg.Store != nil {
		r.Use(accessHTTP.CheckAPIRestriction(configurationhttp.HealthRequest, writeLimitError))
	}
	if cfg.RequestLimits != nil {
		r.Use(limiter.limitPrincipal(limits))
	}
	r.Use(artAPI.HelmPartialReindex)
	r.Use(head.Route)
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
	r.Get("/artifactory/api/system/configuration", sysAPI.GetConfiguration)
	r.Put("/artifactory/api/system/configuration/baseUrl", sysAPI.PutBaseURL)
	// The item routes and the handwritten routes beside them keep their
	// original order (item paths, AQL, agent marketplace, CRAN uploads,
	// transfer, Debian snapshots), and all run before
	// registerHandwrittenStubs: each claims the pattern the spec derives,
	// so it replaces the 501 stub registered under that pattern rather than
	// sitting beside it (decision 0002).
	artAPI.RegisterItemRoutes(r)
	agentpluginshttp.AgentPluginItems{Kit: kit}.RegisterAgentMarketplaceRoutes(r)
	cranOps.RegisterCranRoutes(r)
	artAPI.RegisterTransferRoutes(r)
	replicationOps.RegisterExecutionRoutes(r)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).Group(serviceTrust.RegisterRoutes)
	debianOps.RegisterDebianSnapshotRoutes(r)
	artAPI.RegisterStorageRoutes(r)
	artAPI.RegisterTrashRoutes(r)
	artAPI.RegisterMultipartRoutes(r)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).Group(accessHTTP.RegisterRoutes)
	artAPI.RegisterSearchRoutes(r)
	mavenhttp.MavenAPI{Kit: kit}.RegisterMavenRoutes(r)
	// GitLab subgroup owners span path segments; the generated VCS routes
	// handle the ordinary one-segment forms.
	r.Get(vcsNestedPattern, vcsOps.VCSNested)
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
			Projects:     cfg.Access.Projects,
			CurrentAccess: func(ctx context.Context) (kernel.Principal, accessapi.Decider, error) {
				p, a, err := kit.CurrentAccess(ctx)
				if err != nil {
					return p, nil, fmt.Errorf("refresh Terraform publication authority: %w", err)
				}
				return p, a.Decider, nil
			},
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
	// with the management body limit, and the replay wrapping each keyed
	// operation. The VCS file downloads also keep their request, whose
	// conditional and range headers decide their answer.
	keyed := newKeyedOperations(cfg, artAPI, cleanupOps)
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
				SecurityAPI:        accessHTTP.Security,
				SystemAPI:          sysAPI,
				ArtifactoryAPI:     &artAPI,
				VCSAPI:             &vcsOps,
				NPMAPI:             &npmOps,
				DockerAPI:          &dockerOps,
				DebianAPI:          &debianOps,
				CranAPI:            &cranOps,
				RPMAPI:             &rpmOps,
				PackagePoliciesAPI: &cleanupOps,
				ReplicationAPI:     &replicationOps,
				FederatedAPI:       &federatedOps,
			}, append(keyed.artifactory(), vcshttp.KeepRequest), strictOpts),
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
				keyed.access(),
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
			eventgen.NewStrictHandlerWithOptions(eventService{SubscriptionsAPI: subscriptions}, keyed.event(), eventOpts),
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
				keyed.distribution(),
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
			workergen.NewStrictHandlerWithOptions(workerService{}, keyed.worker(), workerOpts),
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
			pfedgen.NewStrictHandlerWithOptions(pfedService{}, keyed.pfed(), pfedOpts),
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
			gridgen.NewStrictHandlerWithOptions(gridService{}, keyed.grid(), gridOpts),
			gridgen.ChiServerOptions{
				BaseURL:          "/grid",
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
		// artifactweave's own peer protocol (decision 0021), beside the
		// JFrog prefixes and outside the parity oracle; peerGuard verifies
		// every request first. /pair is served (PEER-02); the other
		// operations answer 501 until PEER-04, PEER-05 and FED-01 serve
		// them.
		peerOpts := peergen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}
		peergen.HandlerWithOptions(
			peergen.NewStrictHandlerWithOptions(peerService{pair: pairServer, streams: streamServer(cfg), repos: repoServer}, keyed.peer(), peerOpts),
			peergen.ChiServerOptions{
				BaseURL:          peerBase,
				BaseRouter:       g,
				ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
			},
		)
	})

	pypihttp.PypiAPI{Kit: kit}.RegisterPypiMetadataRoutes(r, artAPI.ProtocolOptions())
	puppethttp.PuppetAPI{Kit: kit}.RegisterPuppetMetadataRoutes(r, artAPI.ProtocolOptions())
	chefhttp.ChefAPI{Kit: kit}.RegisterChefReindex(r)

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
	return npmhttp.NPMLoggedPath(luarockshttp.RedactAPIKey(path))
}

// newAuthnOptions returns Authenticate's options as the router composes
// them: the multipart calls verify themselves, npm's login and logout are
// skipped before the skips the caller adds, the registry decides where a
// registry token is good and which paths challenge anonymous requests,
// npm's configuration operations keep their credential, and Access's and
// the system's public routes answer anyone.
func newAuthnOptions(
	a *authn.Authenticator,
	st accesshttp.Store,
	authorizer accessapi.Authorizer,
	skip ...func(*http.Request) bool,
) accesshttp.AuthnOptions {
	public := accesshttp.PublicRoutes()
	maps.Copy(public, configurationhttp.PublicRoutes())
	return accesshttp.AuthnOptions{
		Auth:               a,
		Store:              st,
		Authorizer:         authorizer,
		SelfAuthenticated:  artifactshttp.SelfAuthenticated(),
		Skip:               append([]func(*http.Request) bool{npmhttp.NPMUserRequest}, skip...),
		RegistryPath:       dockerhttp.IsRegistryPath,
		ChallengeAnonymous: dockerhttp.ChallengesAnonymous,
		CredentialKeepers: []func(context.Context, *http.Request, kernel.Principal) context.Context{
			npmhttp.KeepNPMCredential,
		},
		PublicRoutes: public,
	}
}

// newLimitOptions returns the request-limit middleware's options as the
// router composes them: the spec operations' cost classes, and the probes
// (configurationhttp.HealthRequest) and the recovery API
// (APIRecoveryRequest) for requests no spec operation addresses.
func newLimitOptions(limits *apilimit.Service) limitOptions {
	return limitOptions{
		Operations: costOperations(),
		Health:     configurationhttp.HealthRequest,
		Recovery:   accesshttp.APIRecoveryRequest,
		Limits:     limits,
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
// and appends, permission creates, group updates and keyed requests, whose
// exact bytes the replay's fingerprint digests.
func newDetailOptions() detailOptions {
	access := accesshttp.DetailRules()
	return detailOptions{
		SearchQuery: []func(*http.Request) bool{artifactshttp.KeepsSearchQuery, retentionhttp.KeepsSearchQuery},
		Rewrite:     access.Rewrite,
		RawBody: append(
			[]func(*http.Request) bool{
				artifactshttp.KeepsRepositoryBody,
				artifactshttp.KeepsBuildBody,
				idempotency.Keyed,
			},
			access.RawBody...,
		),
	}
}

// registerHandwrittenStubs gives every spec operation marked x-handwritten a
// 501 route unless a real handler already claimed the same method and
// pattern, or x-native-protocol marks a path owned by a pkgroute mount.
// Generated operations get the same treatment from stubgen; without
// this, a hand-written operation that is specified but not yet implemented
// would fall through to the 404 catch-all and look like an unknown path to
// clients, which is the wrong signal (research 10).
func registerHandwrittenStubs(r chi.Router, logger *slog.Logger) {
	existing := registeredRoutes(r)
	for _, op := range handwrittenOps() {
		for _, pattern := range httpkit.ChiPatterns(op.path) {
			key := op.method + " " + pattern
			if existing[key] {
				continue
			}
			existing[key] = true
			r.Method(op.method, pattern, handwrittenStub(op.method, op.path))
			logger.Debug("hand-written operation stubbed", "method", op.method, "path", op.path)
		}
	}
}

// vcsNestedPattern is the route of the GitLab subgroup forms of the
// generated VCS operations (VCSAPI.VCSNested).
const vcsNestedPattern = "/artifactory/api/vcs/*"

// headServed reports the GET routes that answer a HEAD as their GET does
// (middleware.HeadAsGet): every operation of the generated servers that
// the spec declares as a GET, the subgroup forms of the generated VCS
// operations, which answer as the one-segment forms do, and the
// hand-written routes of handwrittenHeads. A path whose spec declares a
// HEAD of its own keeps that route, because HeadAsGet passes a HEAD that a
// route serves.
func headServed() func(pattern string) bool {
	served := map[string]bool{vcsNestedPattern: true}
	for _, op := range operations.Table {
		if op.Method == http.MethodGet && !op.Handwritten {
			served[op.Pattern] = true
		}
	}
	for pattern := range handwrittenHeads {
		served[pattern] = true
	}
	return func(pattern string) bool { return served[pattern] }
}

// headRunsGetRoute reports the served GET routes whose handler runs a HEAD
// as the GET (middleware.HeadAsGet.RunsGet): the hand-written routes that
// handwrittenHeads marks so. The generated operations' handlers see the
// HEAD, and the VCS downloads among them read it.
func headRunsGetRoute(pattern string) bool { return handwrittenHeads[pattern] == headRunsGet }

// handwrittenOp is a hand-written operation: its method and its full path
// template, the spec's server prefix included.
type handwrittenOp struct{ method, path string }

// handwrittenOps lists the routed hand-written operations of every embedded
// spec. They are parsed and validated once per process: that takes seconds
// under the race detector, and tests build a router per test.
var handwrittenOps = sync.OnceValue(func() []handwrittenOp {
	var ops []handwrittenOp
	for _, name := range api.SpecFiles {
		doc, err := loadEmbeddedSpec(name)
		if err != nil {
			// The bundled documents are validated by make generate and by the
			// spec tests; failing here would only hide a build problem.
			panic(fmt.Sprintf("server: embedded spec %s: %v", name, err))
		}
		prefix := ""
		if len(doc.Servers) > 0 {
			prefix = doc.Servers[0].URL
		}
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				if v, ok := op.Extensions["x-handwritten"]; !ok || v != true {
					continue
				}
				// Native package protocols own these paths through pkgroute's
				// repository-aware wildcard handler.
				if v, ok := op.Extensions["x-native-protocol"]; ok && v == true {
					continue
				}
				// Documentation aliases (JFrog pages that describe a header
				// variant under a path no client sends) stay in the spec for
				// parity but must not shadow real item paths.
				if v, ok := op.Extensions["x-not-routed"]; ok && v == true {
					continue
				}
				// Native protocol routes are claimed by pkgroute, whose
				// repository resolver can use a wildcard prefix.
				if v, ok := op.Extensions["x-native-protocol"]; ok && v == true {
					continue
				}
				ops = append(ops, handwrittenOp{method: method, path: prefix + path})
			}
		}
	}
	return ops
})

// registeredRoutes lists "METHOD pattern" for everything already on the router.
func registeredRoutes(r chi.Router) map[string]bool {
	routes := map[string]bool{}
	_ = chi.Walk(
		r,
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			routes[method+" "+route] = true
			return nil
		},
	)
	return routes
}

func handwrittenStub(method, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apierror.Write(w, errorProfileFor(r.URL.Path), apierror.New(
			http.StatusNotImplemented,
			fmt.Sprintf("%s %s is specified but not implemented yet", method, path),
		))
	}
}

func loadEmbeddedSpec(name string) (*openapi3.T, error) {
	raw, err := api.Specs.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("validate %s: %w", name, err)
	}
	return doc, nil
}

// errorProfileRules select the error response contract from the request
// path. Terraform providers join errors[] entries, Docker reads
// errors[].code, npm prints error, and jfrog-client-go prints the body.
// Order matters: the JFrog operations beside the npm, Terraform, pub and
// Swift registries must match before the registries' own prefixes, and the
// first matching rule wins.
var errorProfileRules = []httpkit.ProfileRule{
	{Match: pathPrefix("/xray/"), Profile: apierror.ProfileXray},
	{
		Match: func(path string) bool {
			return npmhttp.NPMOperation(path) || npmhttp.IsNPMReindex(path) ||
				terraformhttp.IsTerraformReindex(path) ||
				reindexBeside(path, "/artifactory/api/pub/", "/artifactory/api/swift/")
		},
		Profile: apierror.ProfileArtifactory,
	},
	{Match: pathPrefix("/artifactory/api/npm/"), Profile: apierror.ProfileNPM},
	{
		// The middleware's errors (authentication, the request limiter) use
		// the envelope the protocol's own handlers use, so a client meets one
		// format on its paths: dart pub reads the pub v2 error document, and
		// on a 401 or 403 only a Bearer realm="pub" challenge, and SwiftPM and
		// APM read RFC 7807 problem JSON (research 10). 7.x's envelope here
		// is unconfirmed [I].
		Match: below("/artifactory/api/pub/"), Profile: apierror.ProfilePub,
	},
	{Match: below("/artifactory/api/swift/"), Profile: apierror.ProfileSwiftRegistry},
	{Match: below("/artifactory/api/agentpackages/"), Profile: apierror.ProfileAPM},
	{
		Match: func(path string) bool {
			return path == "/artifactory/api/terraform/remote/v2" ||
				strings.HasPrefix(path, "/artifactory/api/terraform/remote/v2/")
		},
		Profile: apierror.ProfileTerraformBackend,
	},
	{Match: pathPrefix("/artifactory/api/terraform/"), Profile: apierror.ProfileTerraformRegistry},
	{Match: pathPrefix("/artifactory/api/go/"), Profile: apierror.ProfileText},
	{
		// Named, though the default gives the same, because research 10
		// once gave PyPI and Helm plain text. 7.x answers Artifactory's
		// envelope under all three (decisions 0037, 0038 and 0039), and
		// pip, twine, NuGet.Protocol and helm read only the status.
		Match: pathPrefix(
			"/artifactory/api/pypi/",
			"/artifactory/api/nuget/",
			"/artifactory/api/helm/",
		),
		Profile: apierror.ProfileArtifactory,
	},
	{
		// The JFrog operations beside the registry (promote,
		// parentManifests) keep the Artifactory envelope (decision 0031).
		Match:   dockerhttp.IsRegistryPath,
		Profile: apierror.ProfileOCI,
	},
	{Match: pathPrefix("/access/", "/event/", "/worker/"), Profile: apierror.ProfileAccess},
	{Match: pathPrefix("/artifactory/api/retention/"), Profile: apierror.ProfileArtifactoryLegacy},
	{
		Match: pathPrefix(
			"/artifactory/api/import/",
			"/artifactory/api/export/",
			"/artifactory/api/artifactexport/",
		),
		Profile: apierror.ProfileImportExport,
	},
	{Match: pathPrefix("/distribution/"), Profile: apierror.ProfileJFrogDistribution},
	{Match: pathPrefix("/pfed/"), Profile: apierror.ProfilePlatformFederation},
	{Match: pathPrefix("/grid/"), Profile: apierror.ProfileGrid},
	// The peer API takes the same flat envelope as /grid: a symbolic code
	// that peers branch on, which /pfed's integer code would only repeat
	// from the status. The spec's ErrorResponse records it.
	{Match: pathPrefix(peerBase + "/"), Profile: apierror.ProfileGrid},
}

// skipsAuthentication reports the requests user authentication passes
// untouched beyond newAuthnOptions' own: the peer API's, the Terraform
// backend's that verify themselves, and those a mounted protocol skips.
func skipsAuthentication(mounted []pkgroute.Protocol) func(*http.Request) bool {
	return func(req *http.Request) bool {
		if peerRequest(req) {
			return true
		}
		if terraformbackendhttp.IsPath(req.URL.Path) {
			return terraformbackendhttp.SkipsAuthentication(req)
		}
		return slices.ContainsFunc(mounted, func(p pkgroute.Protocol) bool {
			return p.SkipsAuthentication(req)
		})
	}
}

// errorProfileFor is the profile errorProfileRules give path. Unclaimed
// routes keep the same profile as handled requests so clients can read
// their 404 responses.
func errorProfileFor(path string) apierror.Profile {
	return httpkit.ProfileFor(errorProfileRules, path)
}

// reindexBeside reports whether path is JFrog's reindex operation beside
// one of the protocols under prefixes, so that it keeps Artifactory's
// envelope as npm's and Terraform's do. Two routes answer for it, and both
// answer in that envelope: the generated <prefix>{repoKey}/reindex, which
// chi matches even with an empty key and whose binder then answers 400, and
// pkgroute's reserved {repoKey}/reindex, which trims slashes and answers the
// operation's other methods 405. A path either route takes matches. The
// rules see the unescaped path, so a key with an encoded slash, which no
// repository key can hold, is missed here as in IsNPMReindex.
func reindexBeside(path string, prefixes ...string) bool {
	for _, p := range prefixes {
		rest, ok := strings.CutPrefix(path, p)
		if !ok {
			continue
		}
		if _, op, _ := strings.Cut(rest, "/"); op == "reindex" {
			return true
		}
		if key, op, _ := strings.Cut(strings.Trim(rest, "/"), "/"); key != "" && op == "reindex" {
			return true
		}
	}
	return false
}

// below matches a path strictly below prefix. The bare prefix names no
// repository, so no protocol route takes it, and its 404 is the router's
// catch-all's, in Artifactory's envelope like any path no route takes.
func below(prefix string) func(path string) bool {
	return func(path string) bool {
		return len(path) > len(prefix) && strings.HasPrefix(path, prefix)
	}
}

// pathPrefix matches a path that starts with any of prefixes.
func pathPrefix(prefixes ...string) func(path string) bool {
	return func(path string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(path, p) {
				return true
			}
		}
		return false
	}
}

// notSupported answers for JFrog products artifactweave deliberately does not
// implement (Xray, Lifecycle, AppTrust, Evidence, Workers, Federation). 501 with
// a product name tells a Terraform or jf user the feature is absent, not broken
// (research 10).
func notSupported(product string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apierror.Write(w, errorProfileFor(r.URL.Path), apierror.NotSupported(product))
	}
}

// notFound is the catch-all. Under /v2 the OCI code is NAME_UNKNOWN because
// that is what registry clients expect for an unknown repository.
func notFound(w http.ResponseWriter, r *http.Request) {
	s := errorProfileFor(r.URL.Path)
	e := apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	if s == apierror.ProfileOCI {
		e.Code = "NAME_UNKNOWN"
	}
	apierror.Write(w, s, e)
}

// methodNotAllowed keeps the envelope rule for 405 too.
func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	apierror.Write(
		w,
		errorProfileFor(r.URL.Path),
		apierror.New(http.StatusMethodNotAllowed, "method not allowed: "+r.Method+" "+r.URL.Path),
	)
}

// The strict servers. Each generated interface is satisfied by a composite
// that embeds its partials at depth 1 and the generated UnimplementedStrict
// at depth 2, inside a stub holder. Go promotes the shallowest method, so
// an operation a partial implements shadows its 501 stub, and one that no
// partial implements still answers 501. Two partials implementing the same
// operation sit at the same depth, which makes the method ambiguous and
// fails the assertion below: the intended guard against a duplicate
// implementation. A partial never embeds the stubs or another partial, and
// state a partial embeds (accessState, itemkit.Kit) defines no method named
// after an operation, or it would collide with the stub at depth 2.

// artifactoryStubs holds the artifactory spec's generated stubs.
type artifactoryStubs struct{ artgen.UnimplementedStrict }

// artifactoryServer is the artifactory spec's strict server: the access
// module's security operations, the system operations, the artifacts
// module's operations, and those of each ecosystem that owns some (VCS,
// npm, Docker, Debian, CRAN and RPM). Every artifacts and ecosystem partial
// embeds the same item kit, whose methods sit at depth 2 beside the stubs
// and are named after no operation.
type artifactoryServer struct {
	*accesshttp.SecurityAPI
	*configurationhttp.SystemAPI
	*artifactshttp.ArtifactoryAPI
	*vcshttp.VCSAPI
	*npmhttp.NPMAPI
	*dockerhttp.DockerAPI
	*debianhttp.DebianAPI
	*cranhttp.CranAPI
	*rpmhttp.RPMAPI
	*retentionhttp.PackagePoliciesAPI
	*federationhttp.ReplicationAPI
	*federationhttp.FederatedAPI
	artifactoryStubs
}

// accessStubs holds the access spec's generated stubs.
type accessStubs struct{ accessgen.UnimplementedStrict }

// accessServer is the access spec's strict server.
type accessServer struct {
	*accesshttp.AccessService
	accessStubs
}

// eventStubs leaves observed delivery tests and troubleshooting with their
// deferred owners. Its extra depth lets the concrete CRUD partial win promotion.
type eventStubs struct{ eventgen.UnimplementedStrict }

// eventService composes subscription administration into /event.
type eventService struct {
	*notificationshttp.SubscriptionsAPI
	eventStubs
}

// The services brought into scope in P1b (research 16) start with every
// operation stubbed; their phases replace the stubs.
type (
	distributionService struct {
		distributiongen.UnimplementedStrict
	}
	workerService struct{ workergen.UnimplementedStrict }
	pfedService   struct{ pfedgen.UnimplementedStrict }
	gridService   struct{ gridgen.UnimplementedStrict }
	// peerService is the peer API's server (decision 0021); its P13
	// tickets replace the stubs. pair serves /pair (PEER-02); streams the
	// stream events and blobs (PEER-04); repos /repositories/{repoKey}
	// (FED-01).
	peerService struct {
		peergen.UnimplementedStrict
		pair    federationpeer.PairServer
		streams federationpeer.StreamServer
		repos   federationpeer.RepositoryServer
	}
)

// pairingAPIs are the service_trust endpoints and the peer API's /pair
// over one pairing service (decision 0021). Without a store the served use
// cases answer 500, as other handlers without theirs do, and /pair keeps
// its 501 stub.
func pairingAPIs(cfg RouterConfig) (federationhttp.ServiceTrustAPI, federationpeer.PairServer) {
	serviceTrust := federationhttp.ServiceTrustAPI{
		Unimplemented: func(method, template string) http.Handler { return handwrittenStub(method, template) },
	}
	var pairServer federationpeer.PairServer
	if cfg.Federation != nil {
		if svc, err := cfg.Federation.Pairing(cfg.PeerRemote); err == nil {
			serviceTrust.Pairing, serviceTrust.Authorize = svc, svc.Authorize
			pairServer.Pairing = svc
		}
	}
	if cfg.Store != nil {
		federatedBase := func(ctx context.Context) (string, error) {
			base, _, err := configdoc.StoredScalar(ctx, cfg.Store.Configs(),
				configdoc.ConfigFederatedRepoURLBase, configdoc.FederatedRepoURLBaseField)
			return base, err //nolint:wrapcheck // names the setting
		}
		serviceTrust.FederatedBase, pairServer.FederatedBase = federatedBase, federatedBase
	}
	return serviceTrust, pairServer
}

// RedeemPairing redeems a pairing nonce (PEER-02); without a pairing
// service it keeps the 501 stub.
func (s peerService) RedeemPairing(
	ctx context.Context,
	req peergen.RedeemPairingRequestObject,
) (peergen.RedeemPairingResponseObject, error) {
	if s.pair.Pairing == nil {
		return s.UnimplementedStrict.RedeemPairing(ctx, req) //nolint:wrapcheck // the generated 501
	}
	return s.pair.RedeemPairing(ctx, req) //nolint:wrapcheck // the strict error handler renders the adapter's errors
}

// CancelPairing drops a calling peer's use case (PEER-02); without a
// pairing service it keeps the 501 stub.
func (s peerService) CancelPairing(
	ctx context.Context,
	req peergen.CancelPairingRequestObject,
) (peergen.CancelPairingResponseObject, error) {
	if s.pair.Pairing == nil {
		return s.UnimplementedStrict.CancelPairing(ctx, req) //nolint:wrapcheck // the generated 501
	}
	return s.pair.CancelPairing(ctx, req) //nolint:wrapcheck // as RedeemPairing
}

// PutRepositoryConfiguration applies a member's federated repository
// configuration (FED-01); without a receiver it keeps the 501 stub.
func (s peerService) PutRepositoryConfiguration(
	ctx context.Context,
	req peergen.PutRepositoryConfigurationRequestObject,
) (peergen.PutRepositoryConfigurationResponseObject, error) {
	if s.repos.Receiver == nil {
		return s.UnimplementedStrict.PutRepositoryConfiguration(ctx, req) //nolint:wrapcheck // the generated 501
	}
	return s.repos.PutRepositoryConfiguration(ctx, req) //nolint:wrapcheck // as RedeemPairing
}

// streamServer serves the peer API's stream events and blobs over the
// runtime's delivery engine. A router without one, such as a test's that
// opens no runtime, keeps the 501 stubs.
func streamServer(cfg RouterConfig) federationpeer.StreamServer {
	if e := cfg.Federation.Streams(); e != nil {
		return federationpeer.StreamServer{Streams: e}
	}
	return federationpeer.StreamServer{}
}

// PushEvents applies a peer's batch (PEER-04); without a delivery engine
// it keeps the 501 stub.
func (s peerService) PushEvents(
	ctx context.Context,
	req peergen.PushEventsRequestObject,
) (peergen.PushEventsResponseObject, error) {
	if s.streams.Streams == nil {
		return s.UnimplementedStrict.PushEvents(ctx, req) //nolint:wrapcheck // the generated 501
	}
	return s.streams.PushEvents(ctx, req) //nolint:wrapcheck // as RedeemPairing
}

// PullEvents serves a peer that pulls (PEER-04); without a delivery
// engine it keeps the 501 stub.
func (s peerService) PullEvents(
	ctx context.Context,
	req peergen.PullEventsRequestObject,
) (peergen.PullEventsResponseObject, error) {
	if s.streams.Streams == nil {
		return s.UnimplementedStrict.PullEvents(ctx, req) //nolint:wrapcheck // the generated 501
	}
	return s.streams.PullEvents(ctx, req) //nolint:wrapcheck // as RedeemPairing
}

// GetBlob serves content a peer's stream refers to (PEER-04); without a
// delivery engine it keeps the 501 stub.
func (s peerService) GetBlob(
	ctx context.Context,
	req peergen.GetBlobRequestObject,
) (peergen.GetBlobResponseObject, error) {
	if s.streams.Streams == nil {
		return s.UnimplementedStrict.GetBlob(ctx, req) //nolint:wrapcheck // the generated 501
	}
	return s.streams.GetBlob(ctx, req) //nolint:wrapcheck // as RedeemPairing
}

var (
	_ artgen.StrictServerInterface          = artifactoryServer{}
	_ accessgen.StrictServerInterface       = accessServer{}
	_ eventgen.StrictServerInterface        = eventService{}
	_ distributiongen.StrictServerInterface = distributionService{}
	_ workergen.StrictServerInterface       = workerService{}
	_ pfedgen.StrictServerInterface         = pfedService{}
	_ gridgen.StrictServerInterface         = gridService{}
	_ peergen.StrictServerInterface         = peerService{}
)
