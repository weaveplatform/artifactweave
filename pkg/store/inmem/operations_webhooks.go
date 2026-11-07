package inmem

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Webhooks implements store.Store.
func (s *Store) Webhooks() store.WebhookStore { return webhookStore{s} }

type webhookStore struct{ s *Store }

func (w webhookStore) hooks() *memtx.Table[string, types.WebhookSubscription] {
	return memtx.TableOf[string, types.WebhookSubscription](w.s.Store, "webhooks")
}

func (w webhookStore) deliveries() *memtx.Table[int64, types.WebhookDelivery] {
	return memtx.TableOf[int64, types.WebhookDelivery](w.s.Store, "webhook_deliveries")
}

func (w webhookStore) Create(ctx context.Context, v types.WebhookSubscription) error {
	return w.s.Do(ctx, func() error {
		if w.hooks().Has(v.Key) {
			return memtx.Conflict("webhook", v.Key)
		}
		w.hooks().Put(v.Key, v)
		return nil
	})
}

func (w webhookStore) Get(ctx context.Context, key string) (types.WebhookSubscription, error) {
	var out types.WebhookSubscription
	err := w.s.Do(ctx, func() error {
		var ok bool
		if out, ok = w.hooks().Get(key); !ok {
			return memtx.NotFound("webhook", key)
		}
		return nil
	})
	return out, err
}

func (w webhookStore) Update(ctx context.Context, v types.WebhookSubscription) error {
	return w.s.Do(ctx, func() error {
		if !w.hooks().Has(v.Key) {
			return memtx.NotFound("webhook", v.Key)
		}
		w.hooks().Put(v.Key, v)
		return nil
	})
}

func (w webhookStore) Delete(ctx context.Context, key string) error {
	return w.s.Do(ctx, func() error {
		if !w.hooks().Del(key) {
			return memtx.NotFound("webhook", key)
		}
		w.deliveries().
			DeleteWhere(func(_ int64, d types.WebhookDelivery) bool { return d.WebhookKey == key })
		return nil
	})
}

func (w webhookStore) List(
	ctx context.Context,
	f store.WebhookFilter,
) ([]types.WebhookSubscription, error) {
	var out []types.WebhookSubscription
	err := w.s.Do(ctx, func() error {
		out = w.hooks().All(func(_ string, v types.WebhookSubscription) bool {
			return (f.Domain == "" || v.Domain == f.Domain) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey) &&
				(!f.EnabledOnly || v.Enabled)
		}, func(a, b types.WebhookSubscription) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (w webhookStore) AddDelivery(
	ctx context.Context,
	d types.WebhookDelivery,
) (types.WebhookDelivery, error) {
	var out types.WebhookDelivery
	err := w.s.Do(ctx, func() error {
		if !w.hooks().Has(d.WebhookKey) {
			return memtx.NotFound("webhook", d.WebhookKey)
		}
		d.ID = w.s.NextID("webhook_deliveries")
		w.deliveries().Put(d.ID, d)
		out, _ = w.deliveries().Get(d.ID)
		return nil
	})
	return out, err
}

// attemptKey sorts deliveries the way the SQL stores do, with an unset time
// as 0.
func attemptKey(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

func (w webhookStore) Deliveries(
	ctx context.Context,
	f store.DeliveryFilter,
) ([]types.WebhookDelivery, error) {
	var out []types.WebhookDelivery
	err := w.s.Do(ctx, func() error {
		out = w.deliveries().All(func(_ int64, d types.WebhookDelivery) bool {
			// Bounds compare at stored precision, as the SQL stores do.
			at := d.AttemptedAt
			return (f.WebhookKey == "" || d.WebhookKey == f.WebhookKey) &&
				(f.From.IsZero() || (!at.IsZero() && at.UnixMicro() >= f.From.UnixMicro())) &&
				(f.To.IsZero() || (!at.IsZero() && at.UnixMicro() < f.To.UnixMicro()))
		}, func(a, b types.WebhookDelivery) int {
			if c := cmp.Compare(attemptKey(b.AttemptedAt), attemptKey(a.AttemptedAt)); c != 0 {
				return c
			}
			return cmp.Compare(b.ID, a.ID)
		})
		out = out[:min(len(out), deliveryLimit(f.Limit))]
		return nil
	})
	return out, err
}

// deliveryLimit applies the default limit to a delivery listing.
func deliveryLimit(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}

func (w webhookStore) PruneDeliveries(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := w.s.Do(ctx, func() error {
		n = w.deliveries().DeleteWhere(func(_ int64, d types.WebhookDelivery) bool {
			return !d.AttemptedAt.IsZero() && d.AttemptedAt.UnixMicro() < before.UnixMicro()
		})
		return nil
	})
	return n, err
}
