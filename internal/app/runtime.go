package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/usage"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	debianhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian/http"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/session"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// repairEvery is how often a mirror's repair loop runs a pass; a variable
// so tests need not wait a minute for one.
var repairEvery = mirror.DefaultRepairInterval

// registerDebianSnapshotWork registers the Debian snapshot handler with the
// work queue. It is a variable so tests can see the kit it is given.
var registerDebianSnapshotWork = debianhttp.RegisterDebianSnapshotWork

// Runtime is what serve holds open: the metadata store, the blob tiers, the
// work queue, the modules, and the background loops (mirror repair, GC and
// the work queue's workers). OpenRuntime opens it and Close releases it.
// Test support reads its exported fields to build a router over the stores
// and modules serve would use.
type Runtime struct {
	// Keys is the master key's keyring, nil without a master key.
	Keys *crypt.Keyring
	// Store is the metadata store, with its pending migrations applied.
	Store *appsql.Store
	// Auth authenticates requests against Store's users and Tokens.
	Auth *authn.Authenticator
	// Tokens issues and verifies access tokens.
	Tokens *token.Service
	// Artifacts is the artifacts module, whose remote sources, remote
	// HTTP pool and registry sources serve hands the server.
	Artifacts *artifacts.Module
	// modules are the access, artifacts and configuration modules over
	// Store, Auth and Tokens; the server is given their APIs and the
	// artifacts module's remote sources beside them.
	modules    modules
	closeStore func() error
	// Blobs is the hot tier's blob store; coldBlobs is the cold tier's,
	// nil when cold_blob configures none.
	Blobs     blob.Store
	coldBlobs blob.Store
	location  usage.Location
	multipart *multipart.Service
	// Work runs derived work after the changes that need it (decision 0033).
	Work      *work.Queue
	mirrors   []*mirror.Store
	admin     *mirrorAdmin
	stopLoops context.CancelFunc
	loops     sync.WaitGroup
}

// RuntimeOptions tune what OpenRuntime builds; the zero value is
// production. A zero field keeps the package default, which is
// production's everywhere but app's own tests (hash_test.go).
type RuntimeOptions struct {
	// PasswordHash is the Argon2id cost of the seeded admin password and
	// of the passwords the authenticator hashes. A lower cost than
	// production's is safe in tests: verification reads the cost from
	// each stored hash, so every password still checks, only sooner.
	PasswordHash authn.HashParams
	// TokenKeyBits is the RSA size of the access-token signing key the
	// first start creates.
	TokenKeyBits int
}

// withDefaults returns o with each zero field set to the package default.
func (o RuntimeOptions) withDefaults() RuntimeOptions {
	if o.PasswordHash == (authn.HashParams{}) {
		o.PasswordHash = authHash
	}
	if o.TokenKeyBits == 0 {
		o.TokenKeyBits = tokenKeyBits
	}
	return o
}

// OpenRuntime opens the store (applying pending migrations), builds both
// blob tiers and the modules, and starts a repair loop for each mirror, the
// garbage collector and the work queue's workers, with the costs opts sets.
// On failure everything opened so far is closed again. The caller closes
// the runtime with Close.
func OpenRuntime(
	ctx context.Context,
	cfg config.Config,
	log *slog.Logger,
	opts RuntimeOptions,
) (*Runtime, error) {
	opts = opts.withDefaults()
	// The budget is the process's: every decode shares it, those of the
	// work queue's workers included, so it is set before they start.
	xzfile.SetBudget(int64(cfg.XZ.DictionaryBudget))
	settings := cfg.Artifacts()
	keys, err := Keyring(cfg)
	if err != nil {
		return nil, err
	}
	st, err := OpenStore(ctx, cfg, appsql.Options{Keyring: keys})
	if err != nil {
		return nil, err
	}
	if err := seedPrincipals(ctx, cfg, st, opts.PasswordHash, log); err != nil {
		return nil, errors.Join(err, st.Close())
	}
	if err := applyBootstrap(ctx, cfg, st, log); err != nil {
		return nil, errors.Join(err, st.Close())
	}
	tokens, err := openTokens(ctx, st, opts.TokenKeyBits)
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	authenticator, err := authn.NewAuthenticator(st.Users(), st.Settings(), authn.Options{
		Hash: opts.PasswordHash, Tokens: tokens, Groups: st.Groups(),
		APIRestrictions: st.APIRestrictions(),
	})
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	hotTier, coldTier := tiers(cfg, st)
	hot, err := hotTier.open(ctx, st, log)
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	cold, err := coldTier.open(ctx, st, log)
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	// JFrog CLI multipart uploads stage their parts under the data directory
	// and append them to a hot-tier blob upload. Staging left by a crash
	// after its session was dropped is swept now; the collector reaps
	// sessions that go stale. The service and the collector are built before
	// the work queue, so before the modules, and take access's store-only
	// bindings, which bind what the access module's API does.
	parts := &multipart.Service{
		Store: st, Blobs: hot.store, Dir: filepath.Join(cfg.DataDir, "multipart"),
		Restrictions: access.RestrictionsOf(st),
	}
	if err := parts.Sweep(ctx); err != nil {
		log.Warn("multipart upload staging sweep failed", "error", err)
	}
	// Registry upload sessions append to hot-tier blob uploads, and their
	// finished uploads wait in the item tree until a manifest links them.
	// Expiry purges them through the item service, only ever from docker,
	// oci and helmoci repositories, whose format takes no settings.
	uploads := &registry.Service{Store: st, Blobs: hot.store, Formats: ecosystem.For}
	// The cold tier is not collected: nothing writes it until archiving (P16),
	// which brings the references its collection would have to trust.
	collector, err := gc.New(st, hot.store, gc.Options{
		Grace: settings.GCGrace,
		Reapers: map[string]func(context.Context, session.Upload) error{
			multipart.Backend: parts.Reap,
			registry.Backend:  uploads.Reap,
		},
		Expirers: []func(context.Context, time.Time) (int, error){uploads.ExpireUploads},
		Tokens:   access.TokensOf(st, tokens),
	})
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	queue, err := work.New(st, work.Options{
		Workers:     cfg.Work.Workers,
		Lease:       cfg.Work.Lease,
		MaxAttempts: cfg.Work.MaxAttempts,
		Log:         log,
	})
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	mods, err := buildModules(cfg, moduleDeps{
		store: st, keys: keys, auth: authenticator, tokens: tokens,
		blobs: hot.store, work: queue, gc: collector, log: log,
	})
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	art := mods.artifacts
	// The formats' derived work runs through the artifacts module's item
	// service, with the format settings the server writes with, and its
	// kinds must be known before Run leases.
	if err := art.RegisterWork(ctx); err != nil {
		return nil, errors.Join(err, st.Close())
	}
	// The loops outlive startup's context and stop only through close, after
	// the HTTP server has drained.
	lctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	rt := &Runtime{
		Keys:       keys,
		Store:      st,
		Auth:       authenticator,
		Tokens:     tokens,
		Artifacts:  mods.artifacts,
		modules:    mods,
		closeStore: st.Close,
		Blobs:      hot.store,
		coldBlobs:  cold.store,
		location:   hotTier.location(),
		multipart:  parts,
		Work:       queue,
		stopLoops:  stop,
	}
	if err := art.OpenRemoteSources(ctx); err != nil {
		stop()
		return nil, errors.Join(err, st.Close())
	}
	if err := registerDebianSnapshotWork(queue, itemkit.New(itemkit.Deps{
		Store: st, Blobs: hot.store, Access: mods.access.API(), Work: queue,
		RemoteSources: art.RemoteSources(), FormatSettings: art.FormatSettings(),
	})); err != nil {
		stop()
		return nil, errors.Join(err, st.Close())
	}
	for _, t := range []struct {
		name string
		m    *mirror.Store
	}{{hotTier.section, hot.mirror}, {coldTier.section, cold.mirror}} {
		if t.m == nil {
			continue
		}
		if rt.admin == nil {
			rt.admin = &mirrorAdmin{}
		}
		// One slot: an optimize while a pass is already queued adds nothing.
		trigger := make(chan struct{}, 1)
		rt.admin.tiers = append(rt.admin.tiers, mirrorTier{t.name, t.m, trigger})
		rt.mirrors = append(rt.mirrors, t.m)
		rt.loops.Go(func() { _ = t.m.RunRepair(lctx, repairEvery, trigger) })
	}
	art.Start(lctx, rt.loops.Go)
	// Stopping hands back the leases of runs it cuts short, so another node
	// or the next start picks them up at once.
	rt.loops.Go(func() { _ = queue.Run(lctx) })
	return rt, nil
}

// formatSettings are the formats' options from cfg's artifacts settings,
// with keys, the master key's keyring, which is not configuration: the
// options the artifacts module builds for the server and its item service,
// for keys rotate, which builds no module, to reseal Terraform states with.
func formatSettings(cfg config.Config, keys *crypt.Keyring) ecosystem.Settings {
	formats := cfg.Artifacts().Formats
	formats.TerraformBackend = terraformbackend.Settings{Keys: keys}
	return formats
}

// Close stops the background loops, waits until ctx ends for mirror
// commits still running after their quorum (so they are recorded rather
// than left pending for the repair worker), and closes the store. It is
// called once, after the HTTP server over the runtime has drained; the
// error joins the mirrors' and the store's.
func (rt *Runtime) Close(ctx context.Context) error {
	rt.modules.artifacts.Close()
	rt.stopLoops()
	rt.loops.Wait()
	errs := make([]error, 0, len(rt.mirrors)+1)
	for _, m := range rt.mirrors {
		errs = append(errs, m.WaitIdle(ctx))
	}
	if err := errors.Join(append(errs, rt.closeStore())...); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
