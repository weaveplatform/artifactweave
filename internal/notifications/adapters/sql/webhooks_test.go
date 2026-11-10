package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	ntfsql "github.com/weaveplatform/artifactweave/internal/notifications/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// webhookStore opens a SQLite store, sealed with the test keyring, that
// holds the notifications module's tables alone and two webhooks: w with a
// handler secret and named with a custom webhook's named secret.
func webhookStore(t *testing.T) *ntfsql.Store {
	t.Helper()
	ctx := context.Background()
	s := ntfsql.New(sqltest.OpenSQLite(t, ntfsql.Module))
	_, err := s.Webhooks().AddDelivery(ctx, model.WebhookDelivery{WebhookKey: "missing"})
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("delivery to a missing webhook: %v", err)
	}
	for _, err := range []error{
		s.Webhooks().Create(ctx, model.WebhookSubscription{Key: "w", Handlers: []model.WebhookHandler{
			{Type: model.HandlerWebhook, Secret: "s"},
		}}),
		s.Webhooks().Create(ctx, model.WebhookSubscription{Key: "named", Handlers: []model.WebhookHandler{
			{Type: model.HandlerCustomWebhook, Secrets: []model.NamedSecret{{Name: "token", Value: "t"}}},
		}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestAccWebhooksFaults injects a failure into the statement after a
// successful one and checks every multi-statement write and read reports
// it instead of half-applying or swallowing it.
func TestAccWebhooksFaults(t *testing.T) {
	cases := []struct {
		name   string
		inject []string
		op     func(context.Context, *ntfsql.Store) error
	}{
		{
			"create webhook secrets",
			[]string{sqltest.FailTrigger("INSERT", "webhook_secrets")},
			func(ctx context.Context, s *ntfsql.Store) error {
				return s.Webhooks().
					Create(ctx, model.WebhookSubscription{Key: "x", Handlers: []model.WebhookHandler{
						{Type: model.HandlerWebhook, Secret: "s"},
					}})
			},
		},
		{
			"create webhook named secret",
			[]string{sqltest.FailTrigger("INSERT", "webhook_secrets")},
			func(ctx context.Context, s *ntfsql.Store) error {
				return s.Webhooks().
					Create(ctx, model.WebhookSubscription{Key: "x", Handlers: []model.WebhookHandler{
						{
							Type:    model.HandlerCustomWebhook,
							Secrets: []model.NamedSecret{{Name: "n", Value: "v"}},
						},
					}})
			},
		},
		{
			"create webhook clear secrets",
			[]string{sqltest.DropTable("webhook_secrets")},
			func(ctx context.Context, s *ntfsql.Store) error {
				return s.Webhooks().Create(ctx, model.WebhookSubscription{Key: "x"})
			},
		},
		{
			"update webhook secrets",
			[]string{sqltest.DropTable("webhook_secrets")},
			func(ctx context.Context, s *ntfsql.Store) error {
				return s.Webhooks().Update(ctx, model.WebhookSubscription{Key: "w"})
			},
		},
		{
			"get webhook secrets",
			[]string{sqltest.DropTable("webhook_secrets")},
			func(ctx context.Context, s *ntfsql.Store) error {
				_, err := s.Webhooks().Get(ctx, "w")
				return err
			},
		},
		{
			"list webhook secrets",
			[]string{sqltest.DropTable("webhook_secrets")},
			func(ctx context.Context, s *ntfsql.Store) error {
				_, err := s.Webhooks().List(ctx, port.WebhookFilter{})
				return err
			},
		},
		{
			"delete webhook",
			[]string{sqltest.FailTrigger("DELETE", "webhooks")},
			func(ctx context.Context, s *ntfsql.Store) error {
				return s.Webhooks().Delete(ctx, "w")
			},
		},
		{
			"delete webhook secrets",
			[]string{sqltest.DropTable("webhook_secrets")},
			func(ctx context.Context, s *ntfsql.Store) error {
				return s.Webhooks().Delete(ctx, "w")
			},
		},
		{
			"add delivery",
			[]string{sqltest.FailTrigger("INSERT", "webhook_deliveries")},
			func(ctx context.Context, s *ntfsql.Store) error {
				_, err := s.Webhooks().AddDelivery(ctx, model.WebhookDelivery{WebhookKey: "w"})
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := webhookStore(t)
			sqltest.Exec(t, s.DB(), c.inject...)
			if err := c.op(ctx, s); err == nil || errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("want an injected failure, got %v", err)
			}
		})
	}
}

// TestAccWebhooksSealed reads the sealed webhook secrets through a second
// store over the same database without the keyring that sealed them: every
// read must refuse rather than return ciphertext.
func TestAccWebhooksSealed(t *testing.T) {
	ctx := context.Background()
	sealed := webhookStore(t)
	s := ntfsql.New(dialect.New(sealed.DB(), sealed.Dialect(), nil))
	for name, op := range map[string]func() error{
		"webhook secret": func() error { _, err := s.Webhooks().Get(ctx, "w"); return err },
		"named secret":   func() error { _, err := s.Webhooks().Get(ctx, "named"); return err },
		"webhook list":   func() error { _, err := s.Webhooks().List(ctx, port.WebhookFilter{}); return err },
	} {
		if err := op(); !errors.Is(err, dialect.ErrNoKeyring) {
			t.Errorf("%s: want ErrNoKeyring, got %v", name, err)
		}
	}
}

// TestAccWebhooksUnmigrated runs every webhook method against a database
// without the schema: each must surface the driver error, never a nil error
// or a misleading ErrNotFound.
func TestAccWebhooksUnmigrated(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenDB(filepath.Join(t.TempDir(), "raw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := ntfsql.New(dialect.New(db, sqlite.Dialect, nil)).Webhooks()
	now := time.Unix(100, 0)
	ops := map[string]error{
		"webhook create": ws.Create(ctx, model.WebhookSubscription{Key: "w"}),
		"webhook get":    sqltest.Err(ws.Get(ctx, "w")),
		"webhook update": ws.Update(ctx, model.WebhookSubscription{Key: "w"}),
		"webhook delete": ws.Delete(ctx, "w"),
		"webhook list": sqltest.Err(
			ws.List(ctx, port.WebhookFilter{Domain: "d", ProjectKey: "p", EnabledOnly: true}),
		),
		"add delivery": sqltest.Err(ws.AddDelivery(ctx, model.WebhookDelivery{WebhookKey: "w"})),
		"deliveries": sqltest.Err(
			ws.Deliveries(ctx, port.DeliveryFilter{WebhookKey: "w", From: now, To: now}),
		),
		"prune deliveries": sqltest.Err(ws.PruneDeliveries(ctx, now)),
	}
	for name, err := range ops {
		if err == nil || errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
