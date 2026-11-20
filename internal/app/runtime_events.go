package app

import (
	"context"
	"fmt"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/events"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// openTokens already established the persisted instance identity. Resolve that
// same origin before module assembly; the bus owns no additional worker loop.
// Consumers added by later modules subscribe before RegisterWork. With none,
// the outbox retains events rather than acknowledging them to a dummy consumer.
func openEventBus(ctx context.Context, store *appsql.Store, queue *work.Queue) (*events.Bus, error) {
	identity, err := store.Instance().Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("event bus instance identity: %w", err)
	}
	bus, err := events.New(store, queue, events.Options{Origin: identity.InstanceID})
	if err != nil {
		return nil, fmt.Errorf("open event bus: %w", err)
	}
	return bus, nil
}
