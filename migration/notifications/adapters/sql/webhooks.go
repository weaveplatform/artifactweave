package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/notifications/core/model"
	"github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// Webhooks returns the webhook store: subscriptions, their handlers' sealed
// secrets and the delivery records (decision 0011).
func (s *Store) Webhooks() port.WebhookStore { return webhookStore{s} }

// webhookStore keeps webhooks, webhook_secrets and webhook_deliveries.
type webhookStore struct{ s *Store }

// purposeWebhookSecret is the sealing purpose of webhook_secrets.value.
const purposeWebhookSecret = "webhook_secrets.value" // #nosec G101 -- a column name, not a credential

// webhookDoc is the document column of a subscription. The handlers'
// secret values are tagged out of JSON, so the document never holds one.
type webhookDoc struct {
	EventTypes []string               `json:"event_types,omitempty"`
	Criteria   model.WebhookCriteria  `json:"criteria"`
	Handlers   []model.WebhookHandler `json:"handlers,omitempty"`
}

const webhookCols = "key, project_key, description, enabled, debug, domain, created_at, updated_at, doc"

func webhookArgs(w model.WebhookSubscription) ([]any, error) {
	doc, err := dialect.ToJSON(
		webhookDoc{EventTypes: w.EventTypes, Criteria: w.Criteria, Handlers: w.Handlers},
	)
	if err != nil {
		return nil, err
	}
	return []any{
		w.Key, w.ProjectKey, w.Description, w.Enabled, w.Debug, w.Domain,
		dialect.Micros(w.Created), dialect.Micros(w.Updated), doc,
	}, nil
}

// secretID is the row id a webhook secret is sealed under.
func secretID(key string, handler int, name string) string {
	return fmt.Sprintf("%s/%d/%s", key, handler, name)
}

// putSecrets replaces a subscription's sealed secrets: each handler's single
// secret under the name "" and each custom webhook secret under its name.
func (w webhookStore) putSecrets(ctx context.Context, v model.WebhookSubscription) error {
	if _, err := w.s.Exec(ctx, "put webhook secrets "+v.Key,
		"DELETE FROM webhook_secrets WHERE webhook_key = ?", v.Key); err != nil {
		return err
	}
	put := func(i int, name, value string) error {
		if value == "" {
			return nil
		}
		sealed, err := w.s.Seal(purposeWebhookSecret, secretID(v.Key, i, name), value)
		if err != nil {
			return err
		}
		_, err = w.s.Exec(ctx, "put webhook secret "+v.Key, dialect.Upsert("webhook_secrets",
			[]string{"webhook_key", "handler_index", "name", "value"},
			[]string{"webhook_key", "handler_index", "name"}), v.Key, i, name, sealed)
		return err
	}
	for i, h := range v.Handlers {
		if err := put(i, "", h.Secret); err != nil {
			return err
		}
		for _, sec := range h.Secrets {
			if err := put(i, sec.Name, sec.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

// fillSecrets opens a subscription's sealed secrets into its handlers.
func (w webhookStore) fillSecrets(ctx context.Context, v *model.WebhookSubscription) error {
	type slot struct {
		handler int
		name    string
	}
	sealed := map[slot][]byte{}
	err := w.s.Rows(
		ctx,
		"get webhook secrets "+v.Key,
		"SELECT handler_index, name, value FROM webhook_secrets WHERE webhook_key = ?",
		[]any{v.Key},
		func(r *sql.Rows) error {
			var (
				k   slot
				val []byte
			)
			err := r.Scan(&k.handler, &k.name, &val)
			sealed[k] = val
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	if err != nil {
		return err
	}
	open := func(i int, name string) (string, error) {
		return w.s.Unseal(purposeWebhookSecret, secretID(v.Key, i, name), sealed[slot{i, name}])
	}
	for i := range v.Handlers {
		h := &v.Handlers[i]
		if h.Secret, err = open(i, ""); err != nil {
			return err
		}
		for j := range h.Secrets {
			if h.Secrets[j].Value, err = open(i, h.Secrets[j].Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanWebhook(row interface{ Scan(...any) error }) (model.WebhookSubscription, error) {
	var (
		v                model.WebhookSubscription
		created, updated sql.NullInt64
		raw              sql.NullString
		doc              webhookDoc
	)
	if err := row.Scan(&v.Key, &v.ProjectKey, &v.Description, &v.Enabled, &v.Debug, &v.Domain,
		&created, &updated, &raw); err != nil {
		return v, err //nolint:wrapcheck // wrapped by the caller
	}
	v.Created, v.Updated = dialect.FromMicros(created), dialect.FromMicros(updated)
	err := dialect.FromJSON(raw, &doc)
	v.EventTypes, v.Criteria, v.Handlers = doc.EventTypes, doc.Criteria, doc.Handlers
	return v, err
}

func (w webhookStore) Create(ctx context.Context, v model.WebhookSubscription) error {
	args, err := webhookArgs(v)
	if err != nil {
		return err
	}
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := w.s.Exec(
			ctx,
			"create webhook "+v.Key,
			dialect.Insert("webhooks", dialect.OpsCols(webhookCols)),
			args...); err != nil {
			return err
		}
		return w.putSecrets(ctx, v)
	})
}

func (w webhookStore) Get(ctx context.Context, key string) (model.WebhookSubscription, error) {
	v, err := scanWebhook(w.s.Q(ctx).QueryRowContext(ctx,
		w.s.Dialect().Rebind("SELECT "+webhookCols+" FROM webhooks WHERE key = ?"), key))
	if err != nil {
		return v, w.s.Wrap("get webhook "+key, err)
	}
	return v, w.fillSecrets(ctx, &v)
}

func (w webhookStore) Update(ctx context.Context, v model.WebhookSubscription) error {
	args, err := webhookArgs(v)
	if err != nil {
		return err
	}
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if err := w.s.ExecOne(
			ctx,
			"update webhook "+v.Key,
			"UPDATE webhooks SET project_key = ?, description = ?, "+
				"enabled = ?, debug = ?, domain = ?, created_at = ?, updated_at = ?, doc = ? WHERE key = ?",
			append(args[1:], v.Key)...); err != nil {
			return err
		}
		return w.putSecrets(ctx, v)
	})
}

func (w webhookStore) Delete(ctx context.Context, key string) error {
	return w.s.Tx(ctx, func(ctx context.Context) error {
		for _, q := range []string{
			"DELETE FROM webhook_secrets WHERE webhook_key = ?",
			"DELETE FROM webhook_deliveries WHERE webhook_key = ?",
		} {
			if _, err := w.s.Exec(ctx, "delete webhook "+key, q, key); err != nil {
				return err
			}
		}
		return w.s.ExecOne(ctx, "delete webhook "+key, "DELETE FROM webhooks WHERE key = ?", key)
	})
}

func (w webhookStore) List(
	ctx context.Context,
	f port.WebhookFilter,
) ([]model.WebhookSubscription, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.Domain != "" {
		where, args = append(where, "domain = ?"), append(args, f.Domain)
	}
	if f.ProjectKey != "" {
		where, args = append(where, "project_key = ?"), append(args, f.ProjectKey)
	}
	if f.EnabledOnly {
		where, args = append(where, "enabled = ?"), append(args, true)
	}
	var out []model.WebhookSubscription
	err := w.s.Rows(ctx, "list webhooks", "SELECT "+webhookCols+" FROM webhooks WHERE "+
		strings.Join(where, " AND ")+" ORDER BY key", args, func(r *sql.Rows) error {
		v, err := scanWebhook(r)
		out = append(out, v)
		return err
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		if err := w.fillSecrets(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

const deliveryCols = "webhook_key, event_id, event_type, attempted_at, status, elapsed_ms, request, response"

func (w webhookStore) AddDelivery(
	ctx context.Context,
	d model.WebhookDelivery,
) (model.WebhookDelivery, error) {
	err := w.s.Tx(ctx, func(ctx context.Context) error {
		ok, err := w.s.Exists(
			ctx,
			"find webhook "+d.WebhookKey,
			"SELECT 1 FROM webhooks WHERE key = ?",
			d.WebhookKey,
		)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("webhook %s: %w", d.WebhookKey, kernel.ErrNotFound)
		}
		d.ID, err = w.s.InsertID(
			ctx,
			"add delivery",
			dialect.Insert("webhook_deliveries", dialect.OpsCols(deliveryCols)),
			d.WebhookKey,
			d.EventID,
			d.EventType,
			dialect.Micros(d.AttemptedAt),
			d.Status,
			dialect.Millis(d.Elapsed),
			dialect.RawJSON(d.Request),
			dialect.RawJSON(d.Response),
		)
		return err
	})
	if err != nil {
		return model.WebhookDelivery{}, err
	}
	return kernel.Normalise(d), nil
}

func (w webhookStore) Deliveries(
	ctx context.Context,
	f port.DeliveryFilter,
) ([]model.WebhookDelivery, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.WebhookKey != "" {
		where, args = append(where, "webhook_key = ?"), append(args, f.WebhookKey)
	}
	if !f.From.IsZero() {
		where, args = append(where, "attempted_at >= ?"), append(args, f.From.UnixMicro())
	}
	if !f.To.IsZero() {
		where, args = append(where, "attempted_at < ?"), append(args, f.To.UnixMicro())
	}
	var out []model.WebhookDelivery
	err := w.s.Rows(
		ctx,
		"list deliveries",
		"SELECT id, "+deliveryCols+" FROM webhook_deliveries WHERE "+
			strings.Join(
				where,
				" AND ",
			)+" ORDER BY COALESCE(attempted_at, 0) DESC, id DESC LIMIT ?",
		append(args, dialect.LimitOr(f.Limit)),
		func(r *sql.Rows) error {
			var (
				v         model.WebhookDelivery
				attempted sql.NullInt64
				elapsed   int64
				req, resp sql.NullString
			)
			err := r.Scan(
				&v.ID,
				&v.WebhookKey,
				&v.EventID,
				&v.EventType,
				&attempted,
				&v.Status,
				&elapsed,
				&req,
				&resp,
			)
			v.AttemptedAt, v.Elapsed = dialect.FromMicros(attempted), dialect.FromMillis(elapsed)
			v.Request, v.Response = dialect.FromRaw(req), dialect.FromRaw(resp)
			out = append(out, v)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}

func (w webhookStore) PruneDeliveries(ctx context.Context, before time.Time) (int64, error) {
	return w.s.Exec(
		ctx,
		"prune deliveries",
		"DELETE FROM webhook_deliveries WHERE attempted_at < ?",
		before.UnixMicro(),
	)
}
