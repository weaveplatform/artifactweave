package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	webhooksvc "github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

const (
	plainFixture = "web01-private-handler-A"
	firstFixture = "web01-private-named-B"
	otherFixture = "web01-private-named-C"
)

func stringPointer(s string) *string { return &s }

func secretInput() webhooksvc.Input {
	return webhooksvc.Input{
		Key: "subscription", Enabled: true, Domain: model.DomainArtifact, EventTypes: []string{"deployed"},
		Handlers: []webhooksvc.HandlerInput{{Type: model.HandlerWebhook, URL: "https://receiver.example/hook", Secret: stringPointer(plainFixture)}},
	}
}

func safeSecretView(t *testing.T, v any) {
	t.Helper()
	data := storekit.Get[[]byte](t)(json.Marshal(v))
	for _, value := range []string{plainFixture, firstFixture, otherFixture} {
		if strings.Contains(string(data), value) {
			t.Fatalf("public projection contains a fixture secret: %s", data)
		}
	}
}

// RunSecrets exercises the presence-aware service over every store backend.
func RunSecrets(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("PlainSecretKeepRotateWipe", func(t *testing.T) { plainSecretUpdates(t, newStore(t)) })
	t.Run("NamedSecretReplacement", func(t *testing.T) { namedSecretUpdates(t, newStore(t)) })
	t.Run("SecretUpdateRollback", func(t *testing.T) { secretRollback(t, newStore(t)) })
	t.Run("SecretInputValidation", func(t *testing.T) { secretValidation(t, newStore(t)) })
}

func plainSecretUpdates(t *testing.T, st port.Store) {
	ctx := t.Context()
	now := time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)
	svc := webhooksvc.Service{Store: st, Now: func() time.Time { return now }}
	in := secretInput()
	created := storekit.Get[webhooksvc.View](t)(svc.Create(ctx, in))
	safeSecretView(t, created)
	if created.Handlers[0].Secret != "" || !created.Handlers[0].SecretConfigured {
		t.Fatalf("create projection: %+v", created.Handlers[0])
	}
	stored := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	if stored.Handlers[0].Secret != plainFixture || stored.Created != now {
		t.Fatal("create did not persist the usable secret and timestamp")
	}
	in.Handlers[0].Secret = nil
	in.Description = "updated metadata"
	now = now.Add(time.Second)
	updated := storekit.Get[webhooksvc.View](t)(svc.Update(ctx, in.Key, in))
	safeSecretView(t, updated)
	stored = storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	if stored.Description != in.Description || stored.Handlers[0].Secret != plainFixture || stored.Created != now.Add(-time.Second) || stored.Updated != now {
		t.Fatal("metadata-only replacement lost secret or creation time")
	}
	in.Handlers[0].Secret = stringPointer(firstFixture)
	storekit.Get[webhooksvc.View](t)(svc.Update(ctx, in.Key, in))
	stored = storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	if stored.Handlers[0].Secret != firstFixture {
		t.Fatal("explicit replacement did not rotate the secret")
	}
	safeSecretView(t, storekit.Get[webhooksvc.View](t)(svc.Get(ctx, in.Key)))
	safeSecretView(t, storekit.Get[[]webhooksvc.View](t)(svc.List(ctx, port.WebhookFilter{})))
	in.Handlers[0].Secret = stringPointer("")
	wiped := storekit.Get[webhooksvc.View](t)(svc.Update(ctx, in.Key, in))
	if wiped.Handlers[0].SecretConfigured {
		t.Fatal("wiped secret remains configured in public view")
	}
	stored = storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	if stored.Handlers[0].Secret != "" {
		t.Fatal("explicit empty did not wipe the secret")
	}
}

func namedSecretUpdates(t *testing.T, st port.Store) {
	ctx := t.Context()
	svc := webhooksvc.Service{Store: st}
	in := secretInput()
	in.Handlers = []webhooksvc.HandlerInput{{Type: model.HandlerCustomWebhook, URL: "https://receiver.example/custom", Secrets: []webhooksvc.NamedSecretInput{
		{Name: "auth", Value: stringPointer(firstFixture)}, {Name: "other", Value: stringPointer(otherFixture)},
	}}}
	created := storekit.Get[webhooksvc.View](t)(svc.Create(ctx, in))
	safeSecretView(t, created)
	if !reflect.DeepEqual(created.Handlers[0].Secrets, []webhooksvc.SecretName{{Name: "auth"}, {Name: "other"}}) {
		t.Fatalf("named secret projection: %+v", created.Handlers[0])
	}
	in.Handlers[0].Secrets = []webhooksvc.NamedSecretInput{{Name: "auth"}, {Name: "other", Value: stringPointer(plainFixture)}}
	storekit.Get[webhooksvc.View](t)(svc.Update(ctx, in.Key, in))
	stored := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	if stored.Handlers[0].Secrets[0].Value != firstFixture || stored.Handlers[0].Secrets[1].Value != plainFixture {
		t.Fatal("named keep/rotation did not merge by name")
	}
	in.Handlers[0].Secrets = []webhooksvc.NamedSecretInput{{Name: "auth"}}
	storekit.Get[webhooksvc.View](t)(svc.Update(ctx, in.Key, in))
	stored = storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	if len(stored.Handlers[0].Secrets) != 1 || stored.Handlers[0].Secrets[0].Value != firstFixture {
		t.Fatal("omitting another name removed or replaced the retained secret")
	}
	in.Handlers[0].Secrets[0].Value = stringPointer("")
	storekit.Get[webhooksvc.View](t)(svc.Update(ctx, in.Key, in))
	stored = storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	if stored.Handlers[0].Secrets[0].Value != "" {
		t.Fatal("explicit empty did not wipe the named value")
	}
	// Replacing the handler type never inherits the other type's secret slots.
	in.Handlers = []webhooksvc.HandlerInput{{Type: model.HandlerWebhook, URL: "https://receiver.example/plain"}}
	v := storekit.Get[webhooksvc.View](t)(svc.Update(ctx, in.Key, in))
	if v.Handlers[0].SecretConfigured || len(v.Handlers[0].Secrets) != 0 {
		t.Fatal("new handler inherited obsolete secret state")
	}
}

type (
	failedSecretStore  struct{ port.Store }
	failedSecretUpdate struct{ port.WebhookStore }
)

func (s failedSecretStore) Webhooks() port.WebhookStore {
	return failedSecretUpdate{s.Store.Webhooks()}
}

func (s failedSecretUpdate) Update(ctx context.Context, w model.WebhookSubscription) error {
	if err := s.WebhookStore.Update(ctx, w); err != nil {
		return fmt.Errorf("write before injected failure: %w", err)
	}
	return kernel.ErrConflict
}

func secretRollback(t *testing.T, st port.Store) {
	ctx := t.Context()
	svc := webhooksvc.Service{Store: st}
	in := secretInput()
	storekit.Get[webhooksvc.View](t)(svc.Create(ctx, in))
	before := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	svc.Store = failedSecretStore{st}
	in.Description, in.Handlers[0].Secret = "must roll back", stringPointer(firstFixture)
	view, err := svc.Update(ctx, in.Key, in)
	if !errors.Is(err, kernel.ErrConflict) || !reflect.DeepEqual(view, webhooksvc.View{}) {
		t.Fatalf("failed update returned partial public configuration: %+v, %v", view, err)
	}
	after := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
	storekit.Same(t, "secret and public configuration rollback", before, after)
	if strings.Contains(err.Error(), firstFixture) || strings.Contains(err.Error(), plainFixture) {
		t.Fatal("update error leaked private values")
	}
}

func secretValidation(t *testing.T, st port.Store) {
	svc, ctx := webhooksvc.Service{Store: st}, t.Context()
	in := secretInput()
	storekit.Get[webhooksvc.View](t)(svc.Create(ctx, in))
	for name, change := range map[string]func(*webhooksvc.Input){
		"rename":           func(in *webhooksvc.Input) { in.Key = "renamed" },
		"project":          func(in *webhooksvc.Input) { in.ProjectKey = stringPointer("another") },
		"no handler":       func(in *webhooksvc.Input) { in.Handlers = nil },
		"unknown handler":  func(in *webhooksvc.Input) { in.Handlers[0].Type = "unknown" },
		"wrong secret set": func(in *webhooksvc.Input) { in.Handlers[0].Secrets = []webhooksvc.NamedSecretInput{{Name: "x"}} },
		"custom raw secret": func(in *webhooksvc.Input) {
			in.Handlers[0].Type = model.HandlerCustomWebhook
		},
		"invalid name": func(in *webhooksvc.Input) {
			in.Handlers[0] = webhooksvc.HandlerInput{Type: model.HandlerCustomWebhook, Secrets: []webhooksvc.NamedSecretInput{{Name: "bad-name", Value: stringPointer(firstFixture)}}}
		},
		"duplicate name": func(in *webhooksvc.Input) {
			in.Handlers[0] = webhooksvc.HandlerInput{Type: model.HandlerCustomWebhook, Secrets: []webhooksvc.NamedSecretInput{{Name: "same"}, {Name: "same"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := kernel.Clone(in)
			change(&bad)
			if _, err := svc.Update(ctx, in.Key, bad); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("invalid update: %v", err)
			}
			stored := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, in.Key))
			if stored.Handlers[0].Secret != plainFixture {
				t.Fatal("invalid update changed a private value")
			}
		})
	}
}
