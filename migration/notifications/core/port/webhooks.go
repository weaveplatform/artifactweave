package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// WebhookFilter narrows subscription listings.
type WebhookFilter struct {
	Domain      types.WebhookDomain
	ProjectKey  string
	EnabledOnly bool
}

// DeliveryFilter narrows delivery records to those attempted at or after
// From and before To. Zero times are open bounds; an empty WebhookKey
// matches every subscription.
type DeliveryFilter struct {
	WebhookKey string
	From       time.Time
	To         time.Time
	Limit      int
}

// WebhookStore persists event subscriptions and their delivery records.
// Handler secrets are sealed.
type WebhookStore interface {
	// Create stores w; ErrConflict when the key exists.
	Create(ctx context.Context, w types.WebhookSubscription) error
	// Get returns the subscription, secrets included.
	Get(ctx context.Context, key string) (types.WebhookSubscription, error)
	// Update replaces the subscription.
	Update(ctx context.Context, w types.WebhookSubscription) error
	// Delete removes the subscription and its delivery records.
	Delete(ctx context.Context, key string) error
	// List returns matching subscriptions ordered by key.
	List(ctx context.Context, f WebhookFilter) ([]types.WebhookSubscription, error)
	// AddDelivery records a delivery attempt and returns it with its ID;
	// ErrNotFound when the subscription does not exist.
	AddDelivery(ctx context.Context, d types.WebhookDelivery) (types.WebhookDelivery, error)
	// Deliveries returns matching records newest first, at most f.Limit
	// (DefaultLimit when zero).
	Deliveries(ctx context.Context, f DeliveryFilter) ([]types.WebhookDelivery, error)
	// PruneDeliveries removes records attempted before t and returns how
	// many.
	PruneDeliveries(ctx context.Context, before time.Time) (int64, error)
}
