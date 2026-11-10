package artifacts

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/smart"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/cocoapods"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/terraformbackend"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
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
	// Work is the work queue that the item service queues derived work on
	// and RegisterWork registers its kinds with.
	Work *work.Queue
	// GC is the garbage collector, whose loop Start runs. app builds it,
	// because its reapers come from app's multipart and registry upload
	// services.
	GC *gc.Collector
	// Settings are the module's settings, from app/config's Artifacts.
	Settings Settings
	// Log receives the logs of the remote runtimes and of the garbage
	// collection loop; nil means slog.Default().
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
	gcInterval            time.Duration
	api                   artifactsapi.API
	// items runs the formats' derived work, and the remote policies ingest
	// through it.
	items *item.Service
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
	formats := d.Settings.Formats
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
		gcInterval:            d.Settings.GCInterval,
		api: artifactsapi.API{
			Repositories: repos.NewRepositories(d.Store, d.Access.Keys, d.Access.Projects),
			Layouts:      layout.Checker{},
		},
		formats: formats,
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
	// Assigned only when present: a nil queue in the interface would be a
	// typed nil, which the item service would call instead of skipping.
	if d.Work != nil {
		m.items.Work = d.Work
	}
	return m, nil
}

// API returns the module's published surface: Repositories and Layouts,
// bound by New.
func (m *Module) API() artifactsapi.API { return m.api }

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
// The errors are the item service's, unwrapped.
func (m *Module) RegisterWork(ctx context.Context) error {
	if err := m.items.RegisterWork(m.work); err != nil {
		return err //nolint:wrapcheck // the item service names its own errors
	}
	return m.items.RegisterCacheMaintenance(ctx, m.work) //nolint:wrapcheck // as above
}

// Start runs the garbage collection loop through goLoop, so the caller that
// cancels ctx can wait for it to return. It runs a pass every
// Settings.GCInterval under the storage-gc lease, and logs each one with
// logGC.
func (m *Module) Start(ctx context.Context, goLoop func(func())) {
	goLoop(func() { _ = m.gc.Loop(ctx, m.gcInterval, logGC(m.log)) })
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
	resolve := func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
		current, err := m.store.Repos().Get(ctx, c.Key)
		if err != nil {
			return model.SourceIdentity{}, source.ErrSuperseded
		}
		if port.RemotePolicyChanged(c, current) {
			return model.SourceIdentity{}, source.ErrSuperseded
		}
		id, err := m.store.RemoteCache().Source(ctx, c.Key)
		if err != nil {
			return model.SourceIdentity{}, fmt.Errorf("remote identity: %w", err)
		}
		return id, nil
	}
	revalidatePrincipal := func(ctx context.Context, q source.Request, permit source.Permit, registry bool) error {
		id, err := resolve(ctx, q.Repo)
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
		if registry && p.Registry != nil {
			p, err = accessAPI.Authenticator.CurrentRegistry(ctx, p)
		} else {
			p, err = accessAPI.Authenticator.Current(ctx, p)
		}
		if err != nil {
			return source.ErrDenied
		}
		_, d, err := accessAPI.Authorizer.Authorize(ctx, p, p.Anonymous)
		if err != nil {
			return source.ErrUnavailable
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
			path = path[:len(path)-len(".sha256")]
		}
		if !allows(
			accessapi.ResourceArtifact,
			accessapi.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
			path,
			accessapi.ActionRead,
		) {
			return source.ErrDenied
		}
		return generic.CheckDownload(
			q.Repo,
			path,
			q.Resource.PermissionFolder || q.Resource.PermissionRoot,
		)
	}
	revalidate := func(ctx context.Context, q source.Request, permit source.Permit) error {
		return revalidatePrincipal(ctx, q, permit, false)
	}
	registryRevalidate := func(ctx context.Context, q source.Request, permit source.Permit) error {
		return revalidatePrincipal(ctx, q, permit, true)
	}
	checkCurrent := func(check func(context.Context, source.Request, source.Permit) error) func(context.Context, remote.Store, source.Request, source.Permit, string) error {
		return func(ctx context.Context, _ remote.Store, q source.Request, permit source.Permit, revision string) error {
			if err := check(ctx, q, permit); err != nil {
				return err
			}
			opts, err := resolver.Options(ctx, q.Repo)
			if err != nil || opts.Revision != revision {
				return source.ErrSuperseded
			}
			return nil
		}
	}
	policy := &remote.Policy{
		Store:           m.store,
		Blobs:           m.blobs,
		Items:           m.items,
		Pool:            &m.remoteHTTP,
		Options:         resolver.Options,
		Key:             key,
		CheckCurrent:    checkCurrent(revalidate),
		GitFetch:        cocoapods.FetchCocoaPodsGit,
		ProjectResponse: nuget.ProjectEmptyFeed,
	}
	registryPolicy := &remote.Policy{
		Store: m.store, Blobs: m.blobs, Items: m.items,
		Options: resolver.Options, CheckCurrent: checkCurrent(registryRevalidate), Key: key,
		GitFetch: cocoapods.FetchCocoaPodsGit, ProjectResponse: nuget.ProjectEmptyFeed,
	}
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
