package sql_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	ntfsql "github.com/weaveplatform/artifactweave/internal/notifications/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func sqlSecretInput(key, secret string) webhook.Input {
	return webhook.Input{Key: key, Enabled: true, Handlers: []webhook.HandlerInput{
		{Type: model.HandlerWebhook, URL: "https://receiver.example/hook", Secret: &secret},
	}}
}

func TestAccWebhookSecretResealPreservesSafeViews(t *testing.T) {
	oldMaterial, newMaterial := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	oldKeys := storekit.Get[*crypt.Keyring](t)(crypt.New(oldMaterial))
	activeKeys := storekit.Get[*crypt.Keyring](t)(crypt.NewKeyring(
		crypt.MasterKey{Version: 1, Material: oldMaterial}, crypt.MasterKey{Version: 2, Material: newMaterial}))
	newKeys := storekit.Get[*crypt.Keyring](t)(crypt.NewKeyring(crypt.MasterKey{Version: 2, Material: newMaterial}))
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			st, ctx := newStore(t), t.Context()
			svc := webhook.Service{Store: st}
			plainValue, firstValue, secondValue := "web01-private-plain", "web01-private-first", "web01-private-second"
			storekit.Get[webhook.View](t)(svc.Create(ctx, sqlSecretInput("plain", plainValue)))
			custom := webhook.Input{Key: "custom", Enabled: true, Handlers: []webhook.HandlerInput{{
				Type:    model.HandlerCustomWebhook,
				Secrets: []webhook.NamedSecretInput{{Name: "first", Value: &firstValue}, {Name: "second", Value: &secondValue}},
			}}}
			storekit.Get[webhook.View](t)(svc.Create(ctx, custom))
			for _, key := range []string{"plain", "custom"} {
				var document string
				storekit.Must(t, st.DB().QueryRowContext(ctx, st.Dialect().Rebind("SELECT doc FROM webhooks WHERE key = ?"), key).Scan(&document))
				for _, secret := range []string{plainValue, firstValue, secondValue} {
					if strings.Contains(document, secret) {
						t.Fatal("subscription document contains a private fixture value")
					}
				}
			}
			var sealed []byte
			storekit.Must(t, st.DB().QueryRowContext(ctx, st.Dialect().Rebind(
				"SELECT value FROM webhook_secrets WHERE webhook_key = ? AND name = ?"), "plain", "").Scan(&sealed))
			if bytes.Contains(sealed, []byte(plainValue)) {
				t.Fatal("secret row was not sealed")
			}
			active := dialect.New(st.DB(), st.Dialect(), activeKeys)
			if n := storekit.Get[int](t)(dialect.Reseal(ctx, active, []dialect.Module{ntfsql.Module})); n != 3 {
				t.Fatalf("resealed %d values, want plain plus two named secrets", n)
			}
			if n := storekit.Get[int](t)(dialect.Reseal(ctx, active, []dialect.Module{ntfsql.Module})); n != 0 {
				t.Fatalf("repeat reseal changed %d active-key values", n)
			}
			retired := ntfsql.New(dialect.New(st.DB(), st.Dialect(), newKeys))
			read := webhook.Service{Store: retired}
			view := storekit.Get[webhook.View](t)(read.Get(ctx, "plain"))
			if view.Handlers[0].Secret != "" || !view.Handlers[0].SecretConfigured {
				t.Fatal("key retirement changed redacted configured state")
			}
			plain := storekit.Get[model.WebhookSubscription](t)(retired.Webhooks().Get(ctx, "plain"))
			named := storekit.Get[model.WebhookSubscription](t)(retired.Webhooks().Get(ctx, "custom"))
			if plain.Handlers[0].Secret != plainValue || named.Handlers[0].Secrets[0].Value != firstValue || named.Handlers[0].Secrets[1].Value != secondValue {
				t.Fatal("reseal or key retirement changed a usable secret or its row binding")
			}
		}, sqltest.WithPostgres(), sqltest.WithKeyring(oldKeys))
}

func TestAccWebhookFailedSecretWriteKeepsPriorState(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			st, ctx := newStore(t), t.Context()
			svc := webhook.Service{Store: st}
			in := sqlSecretInput("hook", "web01-private-before")
			storekit.Get[webhook.View](t)(svc.Create(ctx, in))
			before := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
			if st.Dialect().Name == "postgres" {
				sqltest.Exec(t, st.DB(),
					"CREATE FUNCTION secret_write_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$",
					"CREATE TRIGGER secret_write_failure BEFORE INSERT ON webhook_secrets FOR EACH ROW EXECUTE FUNCTION secret_write_failure()")
			} else {
				sqltest.Exec(t, st.DB(), sqltest.FailTrigger("INSERT", "webhook_secrets"))
			}
			in.Description = "must not survive"
			rotated := "web01-private-after"
			in.Handlers[0].Secret = &rotated
			view, err := svc.Update(ctx, in.Key, in)
			if err == nil || !reflect.DeepEqual(view, webhook.View{}) || strings.Contains(err.Error(), rotated) {
				t.Fatalf("failed write returned public state or private bytes: %+v, %v", view, err)
			}
			after := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
			storekit.Same(t, "configuration and secret rollback after delete/reinsert failure", before, after)
		}, sqltest.WithPostgres())
}

func TestAccWebhookHandlerReplacementRemovesObsoleteSecretRows(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			st, ctx := newStore(t), t.Context()
			svc := webhook.Service{Store: st}
			in := sqlSecretInput("hook", "web01-private-old-handler")
			storekit.Get[webhook.View](t)(svc.Create(ctx, in))
			value := "web01-private-named-handler"
			in.Handlers = []webhook.HandlerInput{{Type: model.HandlerCustomWebhook, Secrets: []webhook.NamedSecretInput{{Name: "auth", Value: &value}}}}
			storekit.Get[webhook.View](t)(svc.Update(ctx, in.Key, in))
			var plainRows int
			storekit.Must(t, st.DB().QueryRowContext(ctx, st.Dialect().Rebind(
				"SELECT COUNT(*) FROM webhook_secrets WHERE webhook_key = ? AND name = ?"), in.Key, "").Scan(&plainRows))
			if plainRows != 0 {
				t.Fatal("handler type change retained its obsolete plain-secret row")
			}
			in.Handlers[0].Secrets = nil
			storekit.Get[webhook.View](t)(svc.Update(ctx, in.Key, in))
			var namedRows int
			storekit.Must(t, st.DB().QueryRowContext(ctx, st.Dialect().Rebind(
				"SELECT COUNT(*) FROM webhook_secrets WHERE webhook_key = ?"), in.Key).Scan(&namedRows))
			if namedRows != 0 {
				t.Fatal("empty custom name set retained obsolete sealed rows")
			}
		}, sqltest.WithPostgres())
}

func TestAccWebhookDeleteDoesNotNeedToUnseal(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			st, ctx := newStore(t), t.Context()
			storekit.Get[webhook.View](t)((webhook.Service{Store: st}).Create(ctx, sqlSecretInput("hook", "web01-private-delete")))
			withoutKeys := ntfsql.New(dialect.New(st.DB(), st.Dialect(), nil))
			if _, err := withoutKeys.Webhooks().Get(ctx, "hook"); err == nil {
				t.Fatal("sealed value unexpectedly opened without a keyring")
			}
			storekit.Must(t, withoutKeys.Webhooks().Delete(ctx, "hook"))
			if _, err := st.Webhooks().Get(ctx, "hook"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("subscription survived key-independent deletion: %v", err)
			}
		}, sqltest.WithPostgres())
}
