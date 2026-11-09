package inmem

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/notifications/core/model"
	"github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// Webhooks returns the webhook store: subscriptions, their handlers' sealed
// secrets and the delivery records (decision 0011).
func (s *Store) Webhooks() port.WebhookStore { return webhookStore{s} }

type webhookStore struct{ s *Store }

func (w webhookStore) hooks() *memtx.Table[string, model.WebhookSubscription] {
	return memtx.TableOf[string, model.WebhookSubscription](w.s.Store, "webhooks")
}

func (w webhookStore) deliveries() *memtx.Table[int64, model.WebhookDelivery] {
	return memtx.TableOf[int64, model.WebhookDelivery](w.s.Store, "webhook_deliveries")
}

func (w webhookStore) Create(ctx context.Context, v model.WebhookSubscription) error {
	return w.s.Do(ctx, func() error {
		if w.hooks().Has(v.Key) {
			return memtx.Conflict("webhook", v.Key)
		}
		w.hooks().Put(v.Key, v)
		return nil
	})
}

func (w webhookStore) Get(ctx context.Context, key string) (model.WebhookSubscription, error) {
	var out model.WebhookSubscription
	err := w.s.Do(ctx, func() error {
		var ok bool
		if out, ok = w.hooks().Get(key); !ok {
			return memtx.NotFound("webhook", key)
		}
		return nil
	})
	return out, err
}

func (w webhookStore) Update(ctx context.Context, v model.WebhookSubscription) error {
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
			DeleteWhere(func(_ int64, d model.WebhookDelivery) bool { return d.WebhookKey == key })
		return nil
	})
}

func (w webhookStore) List(
	ctx context.Context,
	f port.WebhookFilter,
) ([]model.WebhookSubscription, error) {
	var out []model.WebhookSubscription
	err := w.s.Do(ctx, func() error {
		out = w.hooks().All(func(_ string, v model.WebhookSubscription) bool {
			return (f.Domain == "" || v.Domain == f.Domain) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey) &&
				(!f.EnabledOnly || v.Enabled)
		}, func(a, b model.WebhookSubscription) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (w webhookStore) AddDelivery(
	ctx context.Context,
	d model.WebhookDelivery,
) (model.WebhookDelivery, error) {
	var out model.WebhookDelivery
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
	f port.DeliveryFilter,
) ([]model.WebhookDelivery, error) {
	var out []model.WebhookDelivery
	err := w.s.Do(ctx, func() error {
		out = w.deliveries().All(func(_ int64, d model.WebhookDelivery) bool {
			// Bounds compare at stored precision, as the SQL stores do.
			at := d.AttemptedAt
			return (f.WebhookKey == "" || d.WebhookKey == f.WebhookKey) &&
				(f.From.IsZero() || (!at.IsZero() && at.UnixMicro() >= f.From.UnixMicro())) &&
				(f.To.IsZero() || (!at.IsZero() && at.UnixMicro() < f.To.UnixMicro()))
		}, func(a, b model.WebhookDelivery) int {
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
		return kernel.DefaultLimit
	}
	return min(limit, kernel.MaxLimit)
}

func (w webhookStore) PruneDeliveries(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := w.s.Do(ctx, func() error {
		n = w.deliveries().DeleteWhere(func(_ int64, d model.WebhookDelivery) bool {
			return !d.AttemptedAt.IsZero() && d.AttemptedAt.UnixMicro() < before.UnixMicro()
		})
		return nil
	})
	return n, err
}
