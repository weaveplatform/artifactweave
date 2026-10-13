package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// repairEvery is how often a mirror's repair loop runs a pass; a variable
// so tests need not wait a minute for one.
var repairEvery = mirror.DefaultRepairInterval

// runtime is what serve holds open: the metadata store, the blob tiers and
// the mirrors' repair loops.
type runtime struct {
	store            store.Store
	auth             *auth.Authenticator
	closeStore       func() error
	blobs, coldBlobs blob.Store
	mirrors          []*mirror.Store
	stopRepair       context.CancelFunc
	repairing        sync.WaitGroup
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
	authn, err := auth.NewAuthenticator(st.Users(), st.Settings(), auth.Options{})
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
	// The loops outlive startup's context and stop only through close, after
	// the HTTP server has drained.
	rctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	rt := &runtime{
		store: st, auth: authn, closeStore: st.Close, blobs: hot.store, coldBlobs: cold.store,
		stopRepair: stop,
	}
	for _, m := range []*mirror.Store{hot.mirror, cold.mirror} {
		if m == nil {
			continue
		}
		rt.mirrors = append(rt.mirrors, m)
		rt.repairing.Go(func() { _ = m.RunRepair(rctx, repairEvery, nil) })
	}
	return rt, nil
}

// close stops the repair loops, waits until ctx ends for mirror commits still
// running after their quorum (so they are recorded rather than left pending
// for the repair worker), and closes the store.
func (rt *runtime) close(ctx context.Context) error {
	rt.stopRepair()
	rt.repairing.Wait()
	errs := make([]error, 0, len(rt.mirrors)+1)
	for _, m := range rt.mirrors {
		errs = append(errs, m.WaitIdle(ctx))
	}
	if err := errors.Join(append(errs, rt.closeStore())...); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
