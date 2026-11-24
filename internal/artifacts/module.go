package artifacts

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/smart"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/removal"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/transfer"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	platformtx "github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// errNoStore refuses Deps without a store, which every service the module
// builds reads.
var errNoStore = errors.New("artifacts: Deps.Store is required")

// Store is the metadata store the module's services and remote wiring read
// and write: the item service's (item.Store), the repositories binding's
// (repos.Store), the remote policies' (remote.Store) and the credential
// resolver's (upstream.ResolverStore). The leases and settings that guard
// the remote policy key's first creation are the item service's. Every call
// joins the transaction its context carries.
type Store interface {
	item.Store
	repos.Store
	remote.Store
	upstream.ResolverStore
	InTx(context.Context) bool
	// Schedules holds the garbage collector's Quartz schedule (gc.Policies).
	Schedules() schedule.Store
}

// Deps is what app passes New: what the module's services and its remote
// wiring use, all over one metadata store.
type Deps struct {
	// Store is the metadata store, which the item service, the
	// repositories binding and the remote wiring read and write. It is
	// required.
	Store Store
	// Blobs is the hot tier's blob store, which the item service and the
	// remote policies write.
	Blobs blob.Store
	// Keys is the master key's keyring, nil without a master key. The
	// Terraform backend format seals states with it, and the remote wiring
	// derives its policy key from it.
	Keys *crypt.Keyring
	// Access is the access module's API. The module root may not import
	// access core, so the authenticator reaches the module as the API's
	// Authenticator binding.
	Access accessapi.API
	// RepositoryLifecycle resets consumer configuration on a new/deleted key
	// in the repository's metadata transaction. It performs no remote I/O.
	RepositoryLifecycle artifactsapi.RepositoryLifecycle
	// Work is the work queue that the item service queues derived work on
	// and RegisterWork registers its kinds with.
	Work *work.Queue
	// GC is the garbage collector, whose passes ScheduleHandlers runs on
	// gcConfig's schedule. app builds it, because its reapers come from
	// app's multipart and registry upload services.
	GC *gc.Collector
	// Settings are the module's settings, from app/config's Artifacts.
	Settings Settings
	// Log receives the logs of the remote runtimes and of the scheduled
	// garbage collection passes; nil means slog.Default().
	Log *slog.Logger
	// PrepareNIMModelUpload prepares a NIM model remote's cache upload. It
	// lives in nimmodel's http package, which the module root may not import
	// (decision 0044, dependency rules: only an ecosystem's own subtree,
	// artifacts/adapters/http and app import an ecosystem http package), so
	// app passes it in. A NIM model remote calls it on every cached read, so
	// it must be set.
	PrepareNIMModelUpload func(source.Request, *item.CacheUpload)
}

// Module is the artifacts module as app holds it: its API, the item service
// that runs the formats' derived work, and the remote runtimes that
// OpenRemoteSources opens. app calls its methods in OpenRuntime's order
// from one goroutine; the services they open are safe for concurrent use.
type Module struct {
	store                 Store
	blobs                 blob.Store
	keys                  *crypt.Keyring
	access                accessapi.API
	work                  *work.Queue
	gc                    *gc.Collector
	log                   *slog.Logger
	prepareNIMModelUpload func(source.Request, *item.CacheUpload)
	allowedPrivateOrigins []string
	api                   artifactsapi.API
	// items runs the formats' derived work, and the remote policies ingest
	// through it.
	items        *item.Service
	virtualCache *virtual.Cache
	// formats are the formats' options, which the server and the item
	// service share, so a write applies them whether a client or derived
	// work makes it.
	formats         ecosystem.Settings
	mavenEager      *maven.MavenEager
	smartRemote     *smart.Smart
	smartRegistry   *smart.Smart
	remoteHTTP      upstream.Pool
	remoteSources   *source.Service
	registryHTTP    upstream.RegistryPool
	registrySources *source.Service
	// registryPath is the registry path grammar both Smart Remote runtimes
	// are given. Nil silently turns off property synchronisation for
	// registry requests and refuses every side effect a registry token
	// queued, with no error anywhere.
	registryPath func(string) (image, operation, reference string, err error)
}

// New binds the module's API over d (Repositories over the store and the
// access API's Keys and Projects, Layouts from the layout engine) and
// builds the item service and the formats' settings, with the keyring as
// the Terraform backend's keys. It does no I/O: the store, the network and
// the file system are reached only by the methods app calls later. It
// refuses only Deps without a store.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	cache := virtual.NewCache(virtual.CacheOptions{MaxEntries: d.Settings.VirtualCacheEntries, MaxBytes: d.Settings.VirtualCacheBytes, Log: log})
	formats := d.Settings.Formats
	formats.Helm.IndexChanged = func(ctx context.Context, key string) {
		if err := cache.MemberWritten(ctx, d.Store.Repos(), key, helm.IndexPath, false); err != nil {
			log.ErrorContext(ctx, "Helm virtual index invalidation failed", "member", key, "error", err)
		}
	}
	formats.TerraformBackend = terraformbackend.Settings{Keys: d.Keys}
	m := &Module{
		store:                 d.Store,
		blobs:                 d.Blobs,
		keys:                  d.Keys,
		access:                d.Access,
		work:                  d.Work,
		gc:                    d.GC,
		log:                   log,
		prepareNIMModelUpload: d.PrepareNIMModelUpload,
		allowedPrivateOrigins: d.Settings.AllowedPrivateOrigins,
		api: artifactsapi.API{
			Lifetimes:    repos.NewRepositoryLifetimes(d.Store),
			Repositories: repos.NewRepositories(d.Store, d.Access.Keys, d.Access.Projects, d.RepositoryLifecycle),
			Layouts:      layout.Checker{},
			// The policy is read on every deletion, purge and schedule tick,
			// so the binding holds nothing a change could leave stale.
			StoragePolicies: gc.Policies{Store: d.Store},
		},
		formats:      formats,
		virtualCache: cache,
	}
	// The maxUniqueSnapshots cleanups wait for their timers, as 7.x's do.
	m.items = &item.Service{
		Store: d.Store, Blobs: d.Blobs, Formats: formats.For,
		Keys:             d.Access.Keys,
		ReindexAuthorize: reindexauth.New(d.Access.Authenticator, d.Access.Authorizer),
		Periodic: map[string]time.Duration{
			maven.KindSnapshots: d.Settings.MavenSnapshotCleanupInterval,
			nuget.KindSnapshots: d.Settings.NuGetSnapshotCleanupInterval,
		},
	}
	m.items.GuardAuthority = m.guardAuthority
	m.api.PackageIndex = pkgindex.NewReader(d.Store, formats.For, m.items)
	m.api.TransferSource = transfer.Source{
		Store: d.Store, Blobs: d.Blobs, Guard: m.guardAuthority, Current: m.currentTransferCaller,
	}
	m.api.ItemRemoval = removal.New(d.Store, m.items, m.currentRemovalCaller)
	m.api.FederatedItems = federatedItems{store: d.Store, items: m.items}
	m.api.PullSource = pull.Public{Source: pull.Source{
		Content: m.items, InTx: m.store.InTx, Open: m.currentPullSource, Peer: m.openPullPeer, Read: m.readPullContent,
	}}
	// Assigned only when present: a nil queue in the interface would be a
	// typed nil, which the item service would call instead of skipping.
	if d.Work != nil {
		m.items.Work = d.Work
	}
	return m, nil
}

// StoragePoliciesOf returns the StoragePolicies binding over st alone, for
// code that holds a store before any module exists, such as config apply and
// the bootstrap descriptor. It is the binding API().StoragePolicies holds.
func StoragePoliciesOf(st gc.PolicyStore) artifactsapi.StoragePolicies {
	return gc.Policies{Store: st}
}

// API returns the module's repository, layout, package-index, transfer-source and guarded-removal contracts,
// bound by New.
func (m *Module) API() artifactsapi.API { return m.api }

func (m *Module) currentRemovalCaller(ctx context.Context, reference kernel.CredentialReference) (kernel.Principal, accessapi.Decider, error) {
	if m.access.Projects == nil || m.access.Authenticator == nil || m.access.Authorizer == nil {
		return kernel.Principal{}, nil, fmt.Errorf("removal access contracts unavailable: %w", kernel.ErrStore)
	}
	if err := m.access.Projects.Guard(ctx); err != nil {
		return kernel.Principal{}, nil, fmt.Errorf("guard removal authority: %w", err)
	}
	p, err := m.access.Authenticator.Current(ctx, reference.Principal())
	if errors.Is(err, kernel.ErrNotFound) {
		return kernel.Principal{}, nil, kernel.ErrBadCredentials
	}
	if err != nil {
		return kernel.Principal{}, nil, fmt.Errorf("current removal credential: %w", err)
	}
	d, err := m.access.Authorizer.For(ctx, p)
	if err != nil {
		return kernel.Principal{}, nil, fmt.Errorf("current removal permissions: %w", err)
	}
	return p, d, nil
}

func (m *Module) guardAuthority(ctx context.Context) error {
	if m.access.Projects == nil {
		return nil
	}
	// The guard row stays locked until the transaction ends, so a pull
	// session takes it once per transaction rather than once per check.
	_, err := platformtx.Remember(ctx, m.store.InTx(ctx), authorityGuardKey{}, func() (struct{}, error) {
		return struct{}{}, m.access.Projects.Guard(ctx)
	})
	if err != nil {
		return fmt.Errorf("artifact current authority: %w", err)
	}
	return nil
}

type authorityGuardKey struct{}

func (m *Module) currentTransferCaller(ctx context.Context) (accessapi.Decider, error) {
	_, decider, err := m.currentContentCaller(ctx)
	return decider, err
}

// callerKey names one principal's resolved authority within one transaction:
// the WithPrincipal binding it came from and whether it was resolved as a
// registry credential.
type callerKey struct {
	binding  any
	registry bool
	content  bool
}

// rememberCaller resolves a caller's current credential and permissions once
// per transaction, and once for the early checks outside one, within a pull
// replication session (platformtx.WithSession). A pull step checks the same
// caller in every source, cache and publication check it makes, and each read
// the user, groups, restrictions and permissions again: about 130 resolutions
// per fetched file (#1895). Every other read keeps resolving per check.
func (m *Module) rememberCaller(ctx context.Context, key callerKey, resolve func() (accessapi.Decider, error)) (accessapi.Decider, error) {
	if key.binding == nil {
		return resolve()
	}
	return platformtx.Remember(ctx, m.store.InTx(ctx), key, resolve) //nolint:wrapcheck // resolve's own refusals
}

// currentSourceCaller is p's current authority for a remote source check:
// source.ErrDenied when its credential no longer resolves, and
// source.ErrUnavailable when its permissions cannot be read.
func (m *Module) currentSourceCaller(ctx context.Context, p kernel.Principal, registry bool) (accessapi.Decider, error) {
	return m.rememberCaller(ctx, callerKey{binding: kernel.PrincipalBinding(ctx), registry: registry}, func() (accessapi.Decider, error) {
		var err error
		if registry {
			p, err = m.access.Authenticator.CurrentRegistry(ctx, p)
		} else {
			p, err = m.access.Authenticator.Current(ctx, p)
		}
		if err != nil {
			return nil, source.ErrDenied
		}
		_, d, err := m.access.Authorizer.Authorize(ctx, p, p.Anonymous)
		if err != nil {
			return nil, source.ErrUnavailable
		}
		return d, nil
	})
}

func (m *Module) currentContentCaller(ctx context.Context) (kernel.Principal, accessapi.Decider, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return kernel.Principal{}, nil, fmt.Errorf("content source caller: %w", err)
	}
	if m.access.Projects == nil || m.access.Authenticator == nil || m.access.Authorizer == nil {
		return kernel.Principal{}, nil, fmt.Errorf("content source access contracts unavailable: %w", kernel.ErrStore)
	}
	if p.Registry != nil {
		return kernel.Principal{}, nil, kernel.ErrForbidden
	}
	resolve := func() (contentCaller, error) {
		current, err := m.access.Authenticator.Current(ctx, p)
		if errors.Is(err, kernel.ErrNotFound) {
			return contentCaller{}, kernel.ErrBadCredentials
		}
		if err != nil {
			return contentCaller{}, fmt.Errorf("current content source credential: %w", err)
		}
		decider, err := m.access.Authorizer.For(ctx, current)
		if err != nil {
			return contentCaller{}, fmt.Errorf("current content source permissions: %w", err)
		}
		return contentCaller{principal: current, decider: decider}, nil
	}
	key := callerKey{binding: kernel.PrincipalBinding(ctx), content: true}
	caller, err := platformtx.Remember(ctx, m.store.InTx(ctx), key, resolve)
	if err != nil {
		return kernel.Principal{}, nil, err //nolint:wrapcheck // resolve's own refusals
	}
	return caller.principal, caller.decider, nil
}

// remoteConfigKey and remoteIdentityKey name a remote's configuration and
// source identity in a pull session's memo.
type (
	remoteConfigKey   string
	remoteIdentityKey string
)

type contentCaller struct {
	principal kernel.Principal
	decider   accessapi.Decider
}

// VirtualCache is shared by protocol reads and member-write invalidation.
func (m *Module) VirtualCache() *virtual.Cache { return m.virtualCache }

// FormatSettings returns the formats' options that New built, which the
// server writes with and the Debian snapshot work takes.
func (m *Module) FormatSettings() ecosystem.Settings { return m.formats }

// GC returns the garbage collector from Deps, which the storage API runs on
// demand.
func (m *Module) GC() *gc.Collector { return m.gc }

// RemoteHTTP returns the remote repositories' connection pool, through
// which the router drops a deleted repository's connections. It is never
// nil: the pool is a field of the Module, which Close closes.
func (m *Module) RemoteHTTP() interface{ Remove(key string) } { return &m.remoteHTTP }

// RemoteSources returns the remote read service that OpenRemoteSources
// opened, which the generic and Maven-family routes read remote content
// through; nil before it.
func (m *Module) RemoteSources() *source.Service { return m.remoteSources }

// RegistrySources returns the authenticated remote read service of Docker,
// OCI and OCI Helm that OpenRemoteSources opened; nil before it.
func (m *Module) RegistrySources() *source.Service { return m.registrySources }

// RemoteStatus answers Smart Remote's administrator-only diagnostics for
// the path of the repository key, from the runtime that OpenRemoteSources
// started; it must not be called before then.
func (m *Module) RemoteStatus(ctx context.Context, key, path string) (smart.SmartStatus, error) {
	//nolint:wrapcheck // the diagnostics handler maps Smart Remote's own errors
	return m.smartRemote.Status(ctx, key, path)
}

// MavenEager returns the Maven eager-companion queue that OpenRemoteSources
// started, nil before it. Close stops it with the other remote runtimes.
func (m *Module) MavenEager() *maven.MavenEager { return m.mavenEager }

// RegisterWork registers the item service's derived work kinds and its cache
// maintenance with the work queue. app calls it once the queue is open and
// before the queue runs, because a kind with no handler waits (decision
// 0033); the cache maintenance writes its schedule, so it cannot run in New.
// It also creates the garbage collector's Quartz schedule at Artifactory's
// default when no descriptor has set one; an existing schedule is kept, so
// a restart adds none. The errors are the item service's and the
// collector's, unwrapped.
func (m *Module) RegisterWork(ctx context.Context) error {
	if err := m.items.RegisterWork(m.work); err != nil {
		return err //nolint:wrapcheck // the item service names its own errors
	}
	if err := m.items.RegisterCacheMaintenance(ctx, m.work); err != nil {
		return err //nolint:wrapcheck // as above
	}
	return gc.Policies{Store: m.store}.EnsureSchedule(ctx, time.Now()) //nolint:wrapcheck // the collector names its own errors
}

// ScheduleHandlers binds the garbage collector to its Quartz schedule
// (gc.ScheduleKind): each fire of gcConfig.cronExp runs one pass under the
// storage-gc lease, logged with logGC. app merges it with the other
// modules' handlers before the shared scheduler runs.
func (m *Module) ScheduleHandlers() map[string]schedule.Handler {
	return map[string]schedule.Handler{gc.ScheduleKind: m.gc.Scheduled(m.store, logGC(m.log))}
}

// Close stops the Maven eager-companion queue and both Smart Remote runtimes,
// then closes the remote and registry connection pools, in that order. It
// returns once their workers have settled; queued jobs stay in the work
// queue for the next start. A nil Module, or one whose OpenRemoteSources did
// not run, closes as a no-op.
func (m *Module) Close() {
	if m == nil {
		return
	}
	m.mavenEager.Close()
	m.smartRemote.Close()
	m.smartRegistry.Close()
	m.remoteHTTP.Close()
	m.registryHTTP.Close()
}

// logGC logs each scheduled garbage collection pass: one that reclaimed
// something at Info, a failed one at Warn, and idle or skipped ones at Debug.
func logGC(log *slog.Logger) func(gc.Report, error) {
	return func(r gc.Report, err error) {
		switch {
		case err != nil:
			log.Warn("garbage collection failed", "error", err)
		case r.Skipped:
			log.Debug("garbage collection skipped: another instance holds the lease")
		case r.TrashPurged+r.UploadsReaped+r.BlobsDeleted+r.OrphansDeleted == 0 &&
			r.TokensExpired == 0:
			log.Debug("garbage collection: nothing to do")
		default:
			log.Info("garbage collection",
				"trash_purged", r.TrashPurged, "uploads_reaped", r.UploadsReaped,
				"blobs_deleted", r.BlobsDeleted, "orphans_deleted", r.OrphansDeleted,
				"bytes_freed", r.BytesFreed, "kept", r.Kept, "tokens_expired", r.TokensExpired)
		}
	}
}

// fenceStoreFault is err, a failed read of the store inside a remote read's
// authority fence (fence), as a storage fault when a statement the store
// ran failed (kernel.ErrStore), and nil otherwise or once ctx has ended.
// The read then answers 500 "internal error" and the router logs the
// fault, as a failed store call elsewhere in a remote read does (#1470);
// it answered 503 as a superseded read. No 7.x source has this fence;
// 7.161.26 answers a failure of its own database as a server fault, never
// as an upstream's or a changed configuration's [I]
// (https://github.com/weaveplatform/artifactweave/issues/1489).
func fenceStoreFault(ctx context.Context, fence bool, err error) error {
	if !fence || err == nil || ctx.Err() != nil || !errors.Is(err, kernel.ErrStore) {
		return nil
	}
	return item.MarkStorageFault(ctx, err, err) //nolint:wrapcheck // the store's own error, marked
}

// OpenRemoteSources shares the module's transport pool and ingestion service.
// Protocol adapters remain opt-in; generic and Maven-family routes install this.
// It reads the remote policy key, persisting one on the first start without a
// master key, and starts the Maven eager-companion queue and both Smart Remote
// runtimes, which Close stops. app calls it after RegisterWork and before the
// work queue runs.
func (m *Module) OpenRemoteSources(ctx context.Context) error {
	key, err := m.remotePolicyKey(ctx)
	if err != nil {
		return err
	}
	accessAPI := m.access
	resolver, err := upstream.NewResolver(m.store, m.keys, upstream.ResolverOptions{
		RevisionKey: key, AllowedPrivateOrigins: m.allowedPrivateOrigins,
		Certificates: accessAPI.Keys,
	})
	if err != nil {
		return fmt.Errorf("remote resolver: %w", err)
	}
	// identity is c's current source identity. In the authority fence a
	// failed read of the repository is the server's fault when the store
	// failed (fenceStoreFault); around the backend read the source service
	// makes, it supersedes the read as it did.
	identity := func(ctx context.Context, c repo.Config, fence bool) (model.SourceIdentity, error) {
		// A pull session reads the remote's configuration and source identity
		// once per transaction and once for its early checks, as
		// rememberCaller does the caller. Only configuration edits and layout
		// changes, never a pull's own cache writes, replace the identity.
		inTx := m.store.InTx(ctx)
		current, err := platformtx.Remember(ctx, inTx, remoteConfigKey(c.Key), func() (repo.Config, error) {
			return m.store.Repos().Get(ctx, c.Key)
		})
		if err != nil {
			if fault := fenceStoreFault(ctx, fence, err); fault != nil {
				return model.SourceIdentity{}, fault
			}
			return model.SourceIdentity{}, source.ErrSuperseded
		}
		if port.RemotePolicyChanged(c, current) {
			return model.SourceIdentity{}, source.ErrSuperseded
		}
		id, err := platformtx.Remember(ctx, inTx, remoteIdentityKey(c.Key), func() (model.SourceIdentity, error) {
			return m.store.RemoteCache().Source(ctx, c.Key)
		})
		if err != nil {
			return model.SourceIdentity{}, fmt.Errorf("remote identity: %w", err)
		}
		return id, nil
	}
	resolve := func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
		return identity(ctx, c, false)
	}
	revalidatePrincipal := func(ctx context.Context, q source.Request, permit source.Permit, registry, fence bool) error {
		if err := m.guardAuthority(ctx); err != nil {
			return err
		}
		id, err := identity(ctx, q.Repo, fence)
		if err != nil {
			return err
		}
		if id != permit.Identity {
			return source.ErrSuperseded
		}
		p, ok := kernel.FromContext(ctx)
		if !ok {
			return source.ErrDenied
		}
		d, err := m.currentSourceCaller(ctx, p, registry && p.Registry != nil)
		if err != nil {
			return err
		}
		path := q.Resource.Path
		allows := d.Allows
		if q.Resource.PermissionRoot {
			path = ""
		} else if q.Resource.PermissionFolder {
			path = q.Resource.PermissionPath
			allows = d.AllowsFolder
		}
		if q.Resource.ChecksumOnly {
			file, ok := source.ChecksumSubject(path)
			if !ok {
				return source.ErrInvalidRequest
			}
			path = file
		}
		if !allows(
			accessapi.ResourceArtifact,
			accessapi.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
			path,
			accessapi.ActionRead,
		) {
			return source.ErrDenied
		}
		return q.Resource.CheckPolicy(q.Repo, path)
	}
	revalidate := func(ctx context.Context, q source.Request, permit source.Permit) error {
		return revalidatePrincipal(ctx, q, permit, false, false)
	}
	registryRevalidate := func(ctx context.Context, q source.Request, permit source.Permit) error {
		return revalidatePrincipal(ctx, q, permit, true, false)
	}
	// checkCurrent is the authority fence (decision 0032): options that
	// read but changed supersede the read, 503, and so does a failed read
	// of them, unless the store failed to run its statement.
	checkCurrent := func(registry bool) func(context.Context, remote.Store, source.Request, source.Permit, string) error {
		return func(ctx context.Context, _ remote.Store, q source.Request, permit source.Permit, revision string) error {
			if err := revalidatePrincipal(ctx, q, permit, registry, true); err != nil {
				return err
			}
			opts, err := resolver.Options(ctx, q.Repo)
			if err != nil {
				if fault := fenceStoreFault(ctx, true, upstream.StoreFault(err)); fault != nil {
					return fault
				}
				return source.ErrSuperseded
			}
			if opts.Revision != revision {
				return source.ErrSuperseded
			}
			return nil
		}
	}
	policy := &remote.Policy{
		Store:        m.store,
		Blobs:        m.blobs,
		Items:        m.items,
		Pool:         &m.remoteHTTP,
		Options:      resolver.Options,
		Key:          key,
		CheckCurrent: checkCurrent(false),
		GitFetch:     cocoapods.FetchCocoaPodsGit,
		MetadataChanged: func(ctx context.Context, c repo.Config, path string) {
			if c.PackageType == repo.Helm && path == helm.IndexPath {
				m.formats.Helm.IndexChanged(context.WithoutCancel(ctx), c.Key)
			}
		},
		ProjectResponse: nuget.ProjectEmptyFeed,
	}
	registryPolicy := &remote.Policy{
		Store: m.store, Blobs: m.blobs, Items: m.items,
		Options: resolver.Options, CheckCurrent: checkCurrent(true), Key: key,
		GitFetch: cocoapods.FetchCocoaPodsGit, ProjectResponse: nuget.ProjectEmptyFeed,
	}
	policy.GuardAuthority = m.guardAuthority
	registryPolicy.GuardAuthority = m.guardAuthority
	content := &docker.RegistryContent{
		Policy: registryPolicy,
		Registries: &upstream.RegistryClients{
			Pool:    &m.registryHTTP,
			Options: resolver.RegistryOptions,
		},
	}
	content.Install()
	m.registryPath = docker.ParseRegistryPath
	m.registrySources = &source.Service{
		Resolve:    resolve,
		Revalidate: registryRevalidate,
		HTTP:       content,
		Cache:      content,
	}
	mavenBackend := &maven.MavenBackend{Policy: policy}
	policy.PrepareUpload = func(
		ctx context.Context, q source.Request, permit source.Permit,
		opts remote.Options, upload *item.CacheUpload,
	) error {
		if q.Repo.PackageType == repo.NIMModel {
			m.prepareNIMModelUpload(q, upload)
			return nil
		}
		return mavenBackend.PrepareUpload(ctx, q, permit, opts, upload)
	}
	service := &source.Service{
		Resolve:    resolve,
		Revalidate: revalidate,
		HTTP:       mavenBackend,
		Cache:      mavenBackend,
	}
	m.mavenEager, err = maven.NewMavenEager(
		ctx,
		service,
		policy,
		accessAPI.Authenticator,
		accessAPI.Authorizer,
		m.log,
	)
	if err != nil {
		return fmt.Errorf("remote maven companions: %w", err)
	}
	mavenBackend.Eager = m.mavenEager.Queue
	// The two Smart Remote work kinds below name the jobs already queued in
	// the store: renamed, those jobs would find no handler and wait forever
	// (decision 0033), so they stay byte for byte.
	m.smartRemote, err = smart.NewSmart(
		ctx,
		policy,
		&m.remoteHTTP,
		accessAPI.Authenticator,
		accessAPI.Authorizer,
		"remote.smart",
		m.registryPath,
		m.log,
	)
	if err != nil {
		return fmt.Errorf("remote smart runtime: %w", err)
	}
	m.smartRegistry, err = smart.NewSmart(
		ctx,
		registryPolicy,
		&m.remoteHTTP,
		accessAPI.Authenticator,
		accessAPI.Authorizer,
		"remote.smart.registry",
		m.registryPath,
		m.log,
	)
	if err != nil {
		return fmt.Errorf("remote smart runtime: %w", err)
	}
	m.remoteSources = service
	return nil
}

// This internal setting has no public settings endpoint or descriptor mapping.
// Without a master key the database already holds unencrypted authentication
// signing keys. Keep the same posture while preserving cache identity on restart.
// The name is persisted: renamed, the next start would mint a new key and
// change every remote cache identity.
const remotePolicyKeySetting = "remote.internal_policy_key"

func (m *Module) remotePolicyKey(ctx context.Context) ([]byte, error) {
	if m.keys != nil {
		return m.keys.Derive("remote-cache-variants"), nil
	}
	var key []byte
	err := m.store.Tx(ctx, func(tx context.Context) error {
		// Serialize first-start initialization even on PostgreSQL where a missing
		// settings row cannot be locked. The lease is released in this transaction.
		holder := rand.Text()
		now := time.Now().UTC()
		held, err := m.store.Leases().
			Acquire(tx, work.Lease{Name: remotePolicyKeySetting, Holder: holder, Acquired: now, Expires: now.Add(time.Minute)}, now)
		if err != nil {
			return fmt.Errorf("lock remote key: %w", err)
		}
		if !held {
			return source.ErrUnavailable
		}
		err = m.store.Settings().Get(tx, remotePolicyKeySetting, &key)
		if errors.Is(err, kernel.ErrNotFound) {
			key = make([]byte, 32)
			if _, err = rand.Read(key); err != nil {
				return fmt.Errorf("generate remote key: %w", err)
			}
			err = m.store.Settings().Put(tx, remotePolicyKeySetting, key)
		}
		if err != nil {
			return fmt.Errorf("persist remote key: %w", err)
		}
		if len(key) != 32 {
			return source.ErrUnavailable
		}
		return m.store.Leases().Release(tx, remotePolicyKeySetting, holder)
	})
	if err != nil {
		return nil, fmt.Errorf("remote policy key: %w", err)
	}
	return key, nil
}
