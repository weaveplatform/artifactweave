package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// repairEvery is how often a mirror's repair loop runs a pass; a variable
// so tests need not wait a minute for one.
var repairEvery = mirror.DefaultRepairInterval

// runtime is what serve holds open: the metadata store, the blob tiers, the
// garbage collector, the work queue, and the background loops (mirror
// repair, GC and the work queue's workers).
type runtime struct {
	store            store.Store
	auth             *auth.Authenticator
	tokens           *token.Service // issues and verifies access tokens
	closeStore       func() error
	blobs, coldBlobs blob.Store
	location         usage.Location
	gc               *gc.Collector
	multipart        *multipart.Service
	// work runs derived work after the changes that need it (decision 0033).
	work      *work.Queue
	mirrors   []*mirror.Store
	admin     *mirrorAdmin
	stopLoops context.CancelFunc
	loops     sync.WaitGroup
}

// openRuntime opens the store (applying pending migrations), builds both
// blob tiers and starts a repair loop for each mirror. On failure everything
// opened so far is closed again.
func openRuntime(ctx context.Context, cfg config.Config, log *slog.Logger) (*runtime, error) {
	keys, err := Keyring(cfg)
	if err != nil {
		return nil, err
	}
	st, err := OpenStore(ctx, cfg, sqlcommon.Options{Keyring: keys})
	if err != nil {
		return nil, err
	}
	if err := seedPrincipals(ctx, cfg, st, log); err != nil {
		return nil, errors.Join(err, st.Close())
	}
	if err := applyBootstrap(ctx, cfg, st, log); err != nil {
		return nil, errors.Join(err, st.Close())
	}
	tokens, err := openTokens(ctx, st)
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	authn, err := auth.NewAuthenticator(st.Users(), st.Settings(), auth.Options{
		Hash: authHash, Tokens: tokens, Groups: st.Groups(),
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
	// sessions that go stale.
	parts := &multipart.Service{
		Store: st, Blobs: hot.store, Dir: filepath.Join(cfg.DataDir, "multipart"),
	}
	if err := parts.Sweep(ctx); err != nil {
		log.Warn("multipart upload staging sweep failed", "error", err)
	}
	// Registry upload sessions append to hot-tier blob uploads, and their
	// finished uploads wait in the item tree until a manifest links them.
	uploads := &registry.Service{Store: st, Blobs: hot.store}
	// The cold tier is not collected: nothing writes it until archiving (P16),
	// which brings the references its collection would have to trust.
	collector, err := gc.New(st, hot.store, gc.Options{
		Grace: cfg.GC.GracePeriod,
		Reapers: map[string]func(context.Context, store.Upload) error{
			multipart.Backend: parts.Reap,
			registry.Backend:  uploads.Reap,
		},
		Expirers: []func(context.Context, time.Time) (int, error){uploads.ExpireUploads},
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
	// The formats' derived work runs through the same item service, and
	// format settings, the server writes with, and its kinds must be known
	// before Run leases. Maven's snapshot cleanup waits for its timer, as
	// 7.x's does.
	derived := &item.Service{
		Store: st, Blobs: hot.store, Work: queue, Formats: formatSettings(cfg).For,
		Periodic: map[string]time.Duration{
			maven.KindSnapshots: cfg.Maven.SnapshotCleanupInterval,
		},
	}
	if err := derived.RegisterWork(queue); err != nil {
		return nil, errors.Join(err, st.Close())
	}
	// The loops outlive startup's context and stop only through close, after
	// the HTTP server has drained.
	lctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	rt := &runtime{
		store:      st,
		auth:       authn,
		tokens:     tokens,
		closeStore: st.Close,
		blobs:      hot.store,
		coldBlobs:  cold.store,
		location:   hotTier.location(),
		gc:         collector,
		multipart:  parts,
		work:       queue,
		stopLoops:  stop,
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
	rt.loops.Go(func() { _ = collector.Loop(lctx, cfg.GC.Interval, logGC(log)) })
	// Stopping hands back the leases of runs it cuts short, so another node
	// or the next start picks them up at once.
	rt.loops.Go(func() { _ = queue.Run(lctx) })
	return rt, nil
}

// formatSettings are the formats' options from cfg. The server and the
// work queue's item service take the same ones, so a write applies them
// whether a client or derived work makes it.
func formatSettings(cfg config.Config) formats.Settings {
	return formats.Settings{
		NPM: npm.Settings{
			TagLatestByPublish: cfg.NPM.TagLatestByPublish,
			EnforcePath:        cfg.NPM.EnforcePath,
		},
	}
}

// close stops the background loops, waits until ctx ends for mirror commits still
// running after their quorum (so they are recorded rather than left pending
// for the repair worker), and closes the store.
func (rt *runtime) close(ctx context.Context) error {
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
