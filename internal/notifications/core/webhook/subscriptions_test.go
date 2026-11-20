package webhook_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/notifications/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitWebhookSecretContract(t *testing.T) {
	storetest.RunSecrets(t, func(*testing.T) port.Store { return inmem.New(memtx.New()) })
}

func TestUnitWebhookSubscriptionFamiliesAndFutureCriteria(t *testing.T) {
	storetest.RunSubscriptionFamilies(t, func(*testing.T) port.Store { return inmem.New(memtx.New()) })
}

func TestUnitWebhookSubscriptionAdministration(t *testing.T) {
	storetest.RunSubscriptionAdministration(t, func(*testing.T) port.Store { return inmem.New(memtx.New()) })
}

var errFailure = errors.New("injected storage failure")

type brokenStore struct {
	port.Store
	commit bool
}

func (s brokenStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		if s.commit {
			return errFailure
		}
		return nil
	})
}

func (s brokenStore) Webhooks() port.WebhookStore {
	if s.commit {
		return s.Store.Webhooks()
	}
	return brokenWebhooks{s.Store.Webhooks()}
}

type brokenWebhooks struct{ port.WebhookStore }

func (s brokenWebhooks) Create(context.Context, model.WebhookSubscription) error { return errFailure }

func (s brokenWebhooks) Get(context.Context, string) (model.WebhookSubscription, error) {
	return model.WebhookSubscription{Key: "partial"}, errFailure
}

func (s brokenWebhooks) List(context.Context, port.WebhookFilter) ([]model.WebhookSubscription, error) {
	return []model.WebhookSubscription{{Key: "partial"}}, errFailure
}

func testInput() webhook.Input {
	value := "web01-private-unit-value"
	return webhook.Input{Key: "hook", Enabled: true, Handlers: []webhook.HandlerInput{
		{Type: model.HandlerWebhook, URL: "https://receiver.example/hook", Secret: &value},
	}}
}

func TestUnitWebhookSecretFailuresReturnNoPartialProjection(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "commit"}[commit], func(t *testing.T) {
			st := inmem.New(memtx.New())
			svc := webhook.Service{Store: st}
			if _, err := svc.Create(t.Context(), testInput()); err != nil {
				t.Fatal(err)
			}
			svc.Store = brokenStore{Store: st, commit: commit}
			for name, op := range map[string]func() (webhook.View, error){
				"get":    func() (webhook.View, error) { return svc.Get(t.Context(), "hook") },
				"update": func() (webhook.View, error) { return svc.Update(t.Context(), "hook", testInput()) },
				"create": func() (webhook.View, error) { in := testInput(); in.Key = "new"; return svc.Create(t.Context(), in) },
			} {
				view, err := op()
				if !errors.Is(err, errFailure) || !reflect.DeepEqual(view, webhook.View{}) {
					t.Errorf("%s: partial view=%+v err=%v", name, view, err)
				}
			}
			if list, err := svc.List(t.Context(), port.WebhookFilter{}); list != nil || !errors.Is(err, errFailure) {
				t.Errorf("partial list=%+v err=%v", list, err)
			}
		})
	}
}

func TestUnitWebhookSecretValidationAndDetachedViews(t *testing.T) {
	st := inmem.New(memtx.New())
	svc := webhook.Service{Store: st}
	if _, err := svc.Create(t.Context(), webhook.Input{}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("empty identity: %v", err)
	}
	in := testInput()
	key := "alpha"
	in.ProjectKey = &key
	in.EventTypes = []string{"deployed"}
	in.Criteria.RepoKeys = []string{"libs"}
	in.Handlers[0].Headers = []model.Header{{Name: "X-Example", Value: "unchanged"}}
	view, err := svc.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	view.EventTypes[0], view.Criteria.RepoKeys[0], view.Handlers[0].Headers[0].Value = "changed", "changed", "changed"
	stored, err := st.Webhooks().Get(t.Context(), in.Key)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EventTypes[0] != "deployed" || stored.Criteria.RepoKeys[0] != "libs" || stored.Handlers[0].Headers[0].Value != "unchanged" || stored.ProjectKey != key {
		t.Fatalf("projection aliases or lost scope: %+v", stored)
	}
	if _, err := svc.Get(t.Context(), "missing"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing subscription: %v", err)
	}
}
