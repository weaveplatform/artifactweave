package sql_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	ntfsql "github.com/weaveplatform/artifactweave/internal/notifications/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type observedWebhookStore struct {
	*ntfsql.Store
	started chan<- int
	read    chan<- struct{}
	release <-chan struct{}
}

func (s observedWebhookStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context) error {
		var pid int
		if err := s.Q(ctx).QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			return fmt.Errorf("observe webhook transaction: %w", err)
		}
		s.started <- pid
		return fn(ctx)
	})
}

func (s observedWebhookStore) Webhooks() port.WebhookStore {
	return observedWebhookRead{WebhookStore: s.Store.Webhooks(), read: s.read, release: s.release}
}

type observedWebhookRead struct {
	port.WebhookStore
	read    chan<- struct{}
	release <-chan struct{}
}

func (s observedWebhookRead) Get(ctx context.Context, key string) (model.WebhookSubscription, error) {
	w, err := s.WebhookStore.Get(ctx, key)
	if err != nil {
		return w, err
	}
	if err := s.pause(ctx); err != nil {
		return model.WebhookSubscription{}, err
	}
	return w, nil
}

func (s observedWebhookRead) List(ctx context.Context, filter port.WebhookFilter) ([]model.WebhookSubscription, error) {
	w, err := s.WebhookStore.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	if err := s.pause(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

func (s observedWebhookRead) pause(ctx context.Context) error {
	if s.read != nil {
		s.read <- struct{}{}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func webhookWaitsFor(ctx context.Context, st *ntfsql.Store, holder, waiter int, done <-chan struct{}) error {
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := st.DB().QueryRowContext(ctx, "SELECT $1::integer = ANY(pg_blocking_pids($2::integer))", holder, waiter).Scan(&blocked); err != nil {
			return fmt.Errorf("observe webhook lock wait: %w", err)
		}
		if blocked {
			return nil
		}
		select {
		case <-done:
			return fmt.Errorf("webhook waiter completed before acquiring the parent lock: %w", kernel.ErrConflict)
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func webhookBackendPID(t *testing.T, ctx context.Context, started <-chan int) int {
	t.Helper()
	select {
	case pid := <-started:
		return pid
	case <-ctx.Done():
		t.Fatal("webhook transaction did not start before its deadline")
		return 0
	}
}

func TestAccWebhookOmittedSecretWaitsForRotation(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			for _, custom := range []bool{false, true} {
				t.Run(map[bool]string{false: "plain", true: "named"}[custom], func(t *testing.T) {
					st := newStore(t)
					if st.Dialect().Name != "postgres" {
						return // SQLite's serialized writer path is covered by the common service contract.
					}
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					old, rotated := "private-before-rotation", "private-after-rotation"
					in := webhook.Input{Key: "hook", Enabled: true, Handlers: []webhook.HandlerInput{{Type: model.HandlerWebhook, Secret: &old}}}
					if custom {
						in.Handlers[0] = webhook.HandlerInput{
							Type:    model.HandlerCustomWebhook,
							Secrets: []webhook.NamedSecretInput{{Name: "auth", Value: &old}, {Name: "removed", Value: &old}},
						}
					}
					storekit.Get[webhook.View](t)((webhook.Service{Store: st}).Create(ctx, in))
					rotation := kernel.Clone(in)
					if custom {
						rotation.Handlers[0].Secrets[0].Value = &rotated
					} else {
						rotation.Handlers[0].Secret = &rotated
					}
					metadata := kernel.Clone(in)
					metadata.Description = "metadata after rotation"
					metadata.Handlers[0].Secret = nil
					if custom {
						metadata.Handlers[0].Secrets = []webhook.NamedSecretInput{{Name: "auth"}}
					}
					holderStarted, waiterStarted := make(chan int, 1), make(chan int, 1)
					read, release := make(chan struct{}, 1), make(chan struct{})
					releaseNow := sync.OnceFunc(func() { close(release) })
					defer releaseNow()
					holderResult, waiterResult := make(chan error, 1), make(chan error, 1)
					waiterDone := make(chan struct{})
					go func() {
						_, err := (webhook.Service{Store: observedWebhookStore{Store: st, started: holderStarted, read: read, release: release}}).Update(ctx, in.Key, rotation)
						holderResult <- err
					}()
					holder := webhookBackendPID(t, ctx, holderStarted)
					select {
					case <-read:
					case <-ctx.Done():
						t.Fatal("webhook parent read did not complete before its deadline")
					}
					go func() {
						_, err := (webhook.Service{Store: observedWebhookStore{Store: st, started: waiterStarted}}).Update(ctx, in.Key, metadata)
						waiterResult <- err
						close(waiterDone)
					}()
					waiter := webhookBackendPID(t, ctx, waiterStarted)
					storekit.Must(t, webhookWaitsFor(ctx, st, holder, waiter, waiterDone))
					releaseNow()
					storekit.Must(t, <-holderResult)
					storekit.Must(t, <-waiterResult)
					stored := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
					secret := stored.Handlers[0].Secret
					if custom {
						if len(stored.Handlers[0].Secrets) != 1 {
							t.Fatal("concurrent replacement retained an omitted secret name")
						}
						secret = stored.Handlers[0].Secrets[0].Value
					}
					if secret != rotated || stored.Description != metadata.Description {
						t.Fatal("metadata-only update lost a committed secret rotation")
					}
					if custom {
						var obsolete int
						storekit.Must(t, st.DB().QueryRowContext(ctx,
							"SELECT COUNT(*) FROM webhook_secrets WHERE webhook_key = $1 AND name = $2", in.Key, "removed").Scan(&obsolete))
						if obsolete != 0 {
							t.Fatal("removed named secret left an obsolete sealed row")
						}
					}
				})
			}
		}, sqltest.WithPostgres())
}

func TestAccWebhookListAndDeleteUseParentLockOrder(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			for _, listing := range []bool{false, true} {
				t.Run(map[bool]string{false: "update-then-delete", true: "list-then-update"}[listing], func(t *testing.T) {
					st := newStore(t)
					if st.Dialect().Name != "postgres" {
						return
					}
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					in := sqlSecretInput("hook", "web01-private-lock-order")
					storekit.Get[webhook.View](t)((webhook.Service{Store: st}).Create(ctx, in))
					storekit.Get[model.WebhookDelivery](t)(st.Webhooks().AddDelivery(ctx, model.WebhookDelivery{WebhookKey: in.Key}))
					holderStarted, waiterStarted := make(chan int, 1), make(chan int, 1)
					read, release := make(chan struct{}, 1), make(chan struct{})
					releaseNow := sync.OnceFunc(func() { close(release) })
					defer releaseNow()
					holderResult, waiterResult := make(chan error, 1), make(chan error, 1)
					waiterDone := make(chan struct{})
					var listed []webhook.View
					go func() {
						svc := webhook.Service{Store: observedWebhookStore{Store: st, started: holderStarted, read: read, release: release}}
						var err error
						if listing {
							listed, err = svc.List(ctx, port.WebhookFilter{})
						} else {
							_, err = svc.Update(ctx, in.Key, in)
						}
						holderResult <- err
					}()
					holder := webhookBackendPID(t, ctx, holderStarted)
					select {
					case <-read:
					case <-ctx.Done():
						t.Fatal("parent reader did not reach its barrier")
					}
					go func() {
						waiter := observedWebhookStore{Store: st, started: waiterStarted}
						var err error
						if listing {
							wipe := kernel.Clone(in)
							empty := ""
							wipe.Handlers[0].Secret = &empty
							_, err = (webhook.Service{Store: waiter}).Update(ctx, in.Key, wipe)
						} else {
							err = waiter.Tx(ctx, func(ctx context.Context) error { return waiter.Webhooks().Delete(ctx, in.Key) })
						}
						waiterResult <- err
						close(waiterDone)
					}()
					waiter := webhookBackendPID(t, ctx, waiterStarted)
					storekit.Must(t, webhookWaitsFor(ctx, st, holder, waiter, waiterDone))
					releaseNow()
					storekit.Must(t, <-holderResult)
					storekit.Must(t, <-waiterResult)
					if listing {
						current := storekit.Get[webhook.View](t)((webhook.Service{Store: st}).Get(ctx, in.Key))
						if len(listed) != 1 || !listed[0].Handlers[0].SecretConfigured || current.Handlers[0].SecretConfigured {
							t.Fatal("list mixed concurrent handler/secret states")
						}
					} else {
						if _, err := st.Webhooks().Get(ctx, in.Key); !errors.Is(err, kernel.ErrNotFound) {
							t.Fatalf("concurrent delete did not remove subscription: %v", err)
						}
						var children int
						storekit.Must(t, st.DB().QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM webhook_secrets) + (SELECT COUNT(*) FROM webhook_deliveries)").Scan(&children))
						if children != 0 {
							t.Fatal("concurrent delete left secret or delivery rows")
						}
					}
				})
			}
		}, sqltest.WithPostgres())
}

func TestAccWebhookDeliveryWaitsForParentDeletion(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			st := newStore(t)
			if st.Dialect().Name != "postgres" {
				return
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			storekit.Get[webhook.View](t)((webhook.Service{Store: st}).Create(ctx, sqlSecretInput("hook", "web01-private-delivery")))
			holderStarted, waiterStarted := make(chan int, 1), make(chan int, 1)
			locked, release := make(chan struct{}, 1), make(chan struct{})
			releaseNow := sync.OnceFunc(func() { close(release) })
			defer releaseNow()
			holderResult, waiterResult := make(chan error, 1), make(chan error, 1)
			waiterDone := make(chan struct{})
			go func() {
				holder := observedWebhookStore{Store: st, started: holderStarted}
				holderResult <- holder.Tx(ctx, func(ctx context.Context) error {
					var key string
					if err := st.Q(ctx).QueryRowContext(ctx, "SELECT key FROM webhooks WHERE key = $1 FOR UPDATE", "hook").Scan(&key); err != nil {
						return err
					}
					locked <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					return st.Webhooks().Delete(ctx, "hook")
				})
			}()
			holder := webhookBackendPID(t, ctx, holderStarted)
			select {
			case <-locked:
			case <-ctx.Done():
				t.Fatal("deletion did not lock its parent")
			}
			go func() {
				waiter := observedWebhookStore{Store: st, started: waiterStarted}
				waiterResult <- waiter.Tx(ctx, func(ctx context.Context) error {
					_, err := st.Webhooks().AddDelivery(ctx, model.WebhookDelivery{WebhookKey: "hook"})
					return err
				})
				close(waiterDone)
			}()
			waiter := webhookBackendPID(t, ctx, waiterStarted)
			storekit.Must(t, webhookWaitsFor(ctx, st, holder, waiter, waiterDone))
			releaseNow()
			storekit.Must(t, <-holderResult)
			if err := <-waiterResult; !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("delivery after committed deletion returned a foreign-key/store error: %v", err)
			}
		}, sqltest.WithPostgres())
}
