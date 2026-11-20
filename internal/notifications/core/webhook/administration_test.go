package webhook_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

type fullWebhookPolicy struct{}

func (fullWebhookPolicy) Any() bool                  { return true }
func (fullWebhookPolicy) Allows(string, string) bool { return true }

type noWebhookPolicy struct{ fullWebhookPolicy }

func (noWebhookPolicy) Any() bool { return false }

type observedAdministrationStore struct {
	port.Store
	reads *int
}

func (s observedAdministrationStore) Webhooks() port.WebhookStore {
	*s.reads++
	return s.Store.Webhooks()
}

func administrationOperations(ctx context.Context, a webhook.Administration) map[kernel.ScopeActions][]func() error {
	return map[kernel.ScopeActions][]func() error{
		kernel.ScopeRead: {
			func() error { _, err := a.Get(ctx, "hook"); return err },
			func() error { _, err := a.List(ctx, port.WebhookFilter{ProjectKey: "nonexistent"}); return err },
		},
		kernel.ScopeWrite: {
			func() error { _, err := a.Create(ctx, validInput()); return err },
			func() error { return a.Update(ctx, "hook", validInput()) },
		},
		kernel.ScopeDelete: {func() error { return a.Delete(ctx, "hook") }},
	}
}

func TestUnitWebhookAdministrationAuthorizesBeforeAccess(t *testing.T) {
	st := inmem.New(memtx.New())
	reads := 0
	a := webhook.Administration{Service: webhook.Service{Store: observedAdministrationStore{Store: st, reads: &reads}}}
	for _, mode := range []string{"missing", "failure", "nil policy", "empty policy"} {
		t.Run(mode, func(t *testing.T) {
			var asked kernel.ScopeActions
			a.Authorize = func(_ context.Context, action kernel.ScopeActions) (webhook.Policy, error) {
				asked = action
				switch mode {
				case "failure":
					return nil, errFailure
				case "empty policy":
					return noWebhookPolicy{}, nil
				default:
					return nil, nil //nolint:nilnil // a missing policy must fail closed
				}
			}
			if mode == "missing" {
				a.Authorize = nil
			}
			for action, operations := range administrationOperations(t.Context(), a) {
				for _, op := range operations {
					asked = 0
					var want error = kernel.ErrForbidden
					if mode == "failure" {
						want = errFailure
					}
					if err := op(); !errors.Is(err, want) || mode != "missing" && asked != action || reads != 0 {
						t.Fatalf("authority=%v want=%v reads=%d err=%v", asked, action, reads, err)
					}
				}
			}
		})
	}
	_, err := (webhook.Administration{Authorize: func(context.Context, kernel.ScopeActions) (webhook.Policy, error) {
		return fullWebhookPolicy{}, nil
	}}).Create(t.Context(), validInput())
	if !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("missing publication store: %v", err)
	}
}

func TestUnitWebhookAdministrationTransactionFailuresExposeNothing(t *testing.T) {
	st, ctx := inmem.New(memtx.New()), t.Context()
	svc := webhook.Service{Store: st}
	in := validInput()
	if _, err := svc.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	a := webhook.Administration{
		Service: webhook.Service{Store: brokenStore{Store: st, commit: true}},
		Authorize: func(context.Context, kernel.ScopeActions) (webhook.Policy, error) {
			return fullWebhookPolicy{}, nil
		},
	}
	if got, err := a.Get(ctx, in.Key); !errors.Is(err, errFailure) || !reflect.DeepEqual(got, webhook.View{}) {
		t.Fatalf("failed read transaction exposed a projection: %+v %v", got, err)
	}
	if got, err := a.List(ctx, port.WebhookFilter{}); !errors.Is(err, errFailure) || got != nil {
		t.Fatalf("failed list transaction exposed projections: %+v %v", got, err)
	}
	in.Key = "new"
	if got, err := a.Create(ctx, in); !errors.Is(err, errFailure) || !reflect.DeepEqual(got, webhook.View{}) {
		t.Fatalf("failed create transaction exposed a projection: %+v %v", got, err)
	}
	if _, err := svc.Get(ctx, in.Key); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("failed create persisted: %v", err)
	}
	in.Key = "hook"
	rotated := "web02-private-rotation"
	in.Handlers[0].Secret = &rotated
	if err := a.Update(ctx, in.Key, in); !errors.Is(err, errFailure) {
		t.Fatalf("failed update: %v", err)
	}
	before, err := st.Webhooks().Get(ctx, in.Key)
	if err != nil || before.Handlers[0].Secret == rotated {
		t.Fatal("failed update retained secret rotation")
	}
	if err := a.Delete(ctx, in.Key); !errors.Is(err, errFailure) {
		t.Fatalf("failed delete: %v", err)
	}
	if after, err := st.Webhooks().Get(ctx, in.Key); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed delete changed the subscription")
	}
}
