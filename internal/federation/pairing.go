package federation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// PruneNoncesKind is the periodic work that clears expired pairing nonces,
// one item keyed "prune" that runs every PruneNoncesInterval. The kind
// string is persisted in work items, so it never changes.
const (
	PruneNoncesKind     = "federation.pairing-nonce-prune"
	PruneNoncesInterval = 10 * time.Minute
)

// Pairing returns the pairing service (decision 0021) over the module's
// store, calling other instances through remote: the signed peer client.
// kernel.ErrStore when the store has no peer records, outbox or instance
// identity.
func (m *Module) Pairing(remote pairing.Remote) (*pairing.Service, error) {
	store, ok := m.deps.Store.(pairing.Store)
	if !ok {
		return nil, fmt.Errorf("pairing store unavailable: %w", kernel.ErrStore)
	}
	return &pairing.Service{Store: store, Remote: remote, Authorize: m.deps.pairingAdministrator}, nil
}

// pairingAdministrator refuses a caller who is not an administrator now:
// the principal's admin flag first, then the current credential and
// permissions, as 7.161.26's ServiceTrustResource allows ROLE_ADMIN only.
func (d Deps) pairingAdministrator(ctx context.Context) error {
	p, err := kernel.RequireAdmin(ctx)
	if err != nil {
		return fmt.Errorf("pairing administrator: %w", err)
	}
	if d.Access.Projects == nil || d.Access.Authenticator == nil || d.Access.Authorizer == nil {
		return fmt.Errorf("pairing access contracts are unavailable: %w", kernel.ErrStore)
	}
	if err := d.Access.Projects.Guard(ctx); err != nil {
		return fmt.Errorf("guard pairing administration: %w", err)
	}
	p, err = d.Access.Authenticator.Current(ctx, p)
	if errors.Is(err, kernel.ErrNotFound) {
		return kernel.ErrBadCredentials
	}
	if err != nil {
		return fmt.Errorf("current pairing credential: %w", err)
	}
	decider, err := d.Access.Authorizer.For(ctx, p)
	if err != nil {
		return fmt.Errorf("current pairing permissions: %w", err)
	}
	if decider == nil || !decider.IsAdmin() {
		return kernel.ErrForbidden
	}
	return nil
}

// registerPairingWork registers the periodic nonce pruning and queues its
// item when it is not queued yet, so a restart adds none.
func (m *Module) registerPairingWork(ctx context.Context, queue *work.Queue) error {
	svc, err := m.Pairing(nil)
	if err != nil {
		return err
	}
	store, ok := m.deps.Store.(interface{ WorkQueue() work.WorkQueue })
	if !ok {
		return fmt.Errorf("pairing work queue unavailable: %w", kernel.ErrStore)
	}
	enqueue := func(ctx context.Context) error {
		if err := store.WorkQueue().Enqueue(ctx, PruneNoncesKind, "prune", nil, time.Now()); err != nil {
			return fmt.Errorf("queue pairing nonce pruning: %w", err)
		}
		return nil
	}
	if err := queue.HandleEvery(PruneNoncesKind, PruneNoncesInterval, func(ctx context.Context, _ work.WorkItem) error {
		if _, err := svc.Prune(ctx); err != nil {
			return err //nolint:wrapcheck // Prune names its own error
		}
		// The item is done once it returns; queueing the next keeps the
		// schedule going.
		return enqueue(ctx)
	}); err != nil {
		return fmt.Errorf("register pairing nonce pruning: %w", err)
	}
	if _, err := store.WorkQueue().Get(ctx, PruneNoncesKind, "prune"); errors.Is(err, kernel.ErrNotFound) {
		return enqueue(ctx)
	} else if err != nil {
		return fmt.Errorf("read pairing nonce pruning: %w", err)
	}
	return nil
}
