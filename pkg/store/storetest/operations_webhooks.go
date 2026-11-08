package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runWebhooks covers webhooks (decision 0011).
func runWebhooks(t *testing.T, newStore Factory) {
	t.Run("Webhooks", func(t *testing.T) { testWebhooks(t, newStore(t)) })
	t.Run("WebhookDeliveries", func(t *testing.T) { testWebhookDeliveries(t, newStore(t)) })
}

func webhook(
	key string,
	domain types.WebhookDomain,
	project string,
	enabled bool,
) types.WebhookSubscription {
	return types.WebhookSubscription{
		Key: key, ProjectKey: project, Description: "hook " + key, Enabled: enabled, Domain: domain,
		EventTypes: []string{"deployed", "deleted"},
		Criteria: types.WebhookCriteria{
			AnyLocal: true, RepoKeys: []string{"libs"}, IncludePatterns: []string{"org/**"},
			SelectedReleaseBundles: map[string][]string{"app": {"1.0"}},
		},
		Handlers: []types.WebhookHandler{
			{
				Type:                types.HandlerWebhook,
				URL:                 "https://hooks.example/" + key,
				Secret:              "s3cret-" + key,
				UseSecretForSigning: true,
				Headers:             []types.Header{{Name: "X-Team", Value: "build"}},
			},
			{
				Type:    types.HandlerCustomWebhook,
				URL:     "https://custom.example/" + key,
				Method:  "POST",
				Payload: `{"name": "{{.data.name}}"}`,
				Secrets: []types.NamedSecret{
					{Name: "token", Value: "t-" + key},
					{Name: "user", Value: "u"},
				},
			},
		},
		Created: storekit.At(0), Updated: storekit.At(1),
	}
}

func testWebhooks(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Webhooks()
	w1 := webhook("w1", types.DomainArtifact, "", true)
	w2 := webhook("w2", types.DomainBuild, "p1", false)
	w3 := webhook("w3", types.DomainArtifact, "p1", true)
	for _, w := range []types.WebhookSubscription{w3, w1, w2} {
		storekit.Must(t, ws.Create(ctx, w))
	}
	storekit.WantErr(t, ws.Create(ctx, w1), store.ErrConflict)

	storekit.Same(t, "Get", w1, storekit.Get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")))
	_, err := ws.Get(ctx, "nope")
	storekit.WantErr(t, err, store.ErrNotFound)

	list := func(f store.WebhookFilter) []types.WebhookSubscription {
		return storekit.Get[[]types.WebhookSubscription](t)(ws.List(ctx, f))
	}
	storekit.Same(t, "List", []types.WebhookSubscription{w1, w2, w3}, list(store.WebhookFilter{}))
	storekit.Same(
		t,
		"by domain",
		[]types.WebhookSubscription{w1, w3},
		list(store.WebhookFilter{Domain: types.DomainArtifact}),
	)
	storekit.Same(
		t,
		"by project",
		[]types.WebhookSubscription{w2, w3},
		list(store.WebhookFilter{ProjectKey: "p1"}),
	)
	storekit.Same(
		t,
		"enabled",
		[]types.WebhookSubscription{w1, w3},
		list(store.WebhookFilter{EnabledOnly: true}),
	)

	got := storekit.Get[types.WebhookSubscription](t)(ws.Get(ctx, "w1"))
	got.Handlers[0].Headers[0].Value = "mutated"
	got.Handlers[1].Secrets[0].Value = "mutated"
	storekit.Same(
		t,
		"Get is a copy",
		w1,
		storekit.Get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")),
	)

	// Secrets are replaced with the subscription: a changed value, a
	// removed named secret and a cleared handler secret.
	w1.Handlers[0].Secret = ""
	w1.Handlers[1].Secrets = []types.NamedSecret{{Name: "token", Value: "rotated"}}
	w1.Enabled, w1.Updated = false, storekit.At(5)
	storekit.Must(t, ws.Update(ctx, w1))
	storekit.Same(
		t,
		"after Update",
		w1,
		storekit.Get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")),
	)
	storekit.WantErr(
		t,
		ws.Update(ctx, webhook("nope", types.DomainArtifact, "", true)),
		store.ErrNotFound,
	)

	storekit.Must(t, ws.Delete(ctx, "w1"))
	storekit.WantErr(t, ws.Delete(ctx, "w1"), store.ErrNotFound)
	_, err = ws.Get(ctx, "w1")
	storekit.WantErr(t, err, store.ErrNotFound)
	// Re-creating the key starts with no secrets from the deleted one.
	bare := types.WebhookSubscription{
		Key: "w1", Domain: types.DomainArtifact,
		Handlers: []types.WebhookHandler{{Type: types.HandlerWebhook, URL: "https://x"}},
	}
	storekit.Must(t, ws.Create(ctx, bare))
	storekit.Same(
		t,
		"re-created",
		bare,
		storekit.Get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")),
	)
}

func testWebhookDeliveries(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Webhooks()
	storekit.Must(
		t,
		ws.Create(ctx, types.WebhookSubscription{Key: "a", Domain: types.DomainArtifact}),
	)
	storekit.Must(
		t,
		ws.Create(ctx, types.WebhookSubscription{Key: "b", Domain: types.DomainArtifact}),
	)
	delivery := func(key string, minute int, status int) types.WebhookDelivery {
		return types.WebhookDelivery{
			WebhookKey:  key,
			EventID:     key + "-event",
			EventType:   "deployed",
			AttemptedAt: storekit.At(minute),
			Status:      status,
			Elapsed:     1234567 * time.Microsecond,
			Request:     []byte(`{"url":"https://hooks.example"}`),
			Response:    []byte(`{"status":200}`),
		}
	}
	_, err := ws.AddDelivery(ctx, delivery("nope", 0, 200))
	storekit.WantErr(t, err, store.ErrNotFound)

	var added []types.WebhookDelivery
	var last int64
	for _, d := range []types.WebhookDelivery{
		delivery("a", 1, 200), delivery("a", 3, 500), delivery("b", 2, 200), delivery("a", 3, 200),
	} {
		got := storekit.Get[types.WebhookDelivery](t)(ws.AddDelivery(ctx, d))
		if got.ID <= last {
			t.Fatalf("delivery ID %d after %d", got.ID, last)
		}
		last, d.ID = got.ID, got.ID
		storekit.Same(t, "AddDelivery", d, got)
		added = append(added, d)
	}
	a1, a3, b2, a3b := added[0], added[1], added[2], added[3]
	if got := storekit.Get[types.WebhookDelivery](
		t,
	)(
		ws.AddDelivery(ctx, delivery("b", 9, 0)),
	); got.Elapsed != 1234*time.Millisecond {
		t.Fatalf("elapsed = %v, want milliseconds", got.Elapsed)
	}
	deliveries := func(f store.DeliveryFilter) []types.WebhookDelivery {
		return storekit.Get[[]types.WebhookDelivery](t)(ws.Deliveries(ctx, f))
	}
	b9 := delivery("b", 9, 0)
	b9.ID = last + 1
	all := deliveries(store.DeliveryFilter{})
	if len(all) != 5 {
		t.Fatalf("Deliveries = %d records", len(all))
	}
	b9.ID = all[0].ID
	storekit.Same(t, "newest first", []types.WebhookDelivery{b9, a3b, a3, b2, a1}, all)
	storekit.Same(
		t,
		"by webhook",
		[]types.WebhookDelivery{a3b, a3, a1},
		deliveries(store.DeliveryFilter{WebhookKey: "a"}),
	)
	storekit.Same(t, "window", []types.WebhookDelivery{b2},
		deliveries(store.DeliveryFilter{From: storekit.At(2), To: storekit.At(3)}))
	storekit.Same(
		t,
		"from",
		[]types.WebhookDelivery{b9, a3b, a3},
		deliveries(store.DeliveryFilter{From: storekit.At(3)}),
	)
	storekit.Same(
		t,
		"limit",
		[]types.WebhookDelivery{b9, a3b},
		deliveries(store.DeliveryFilter{Limit: 2}),
	)

	// A delivery without a time sorts after every timed one, falls outside
	// every window and is never pruned by age.
	untimed := delivery("b", 0, 0)
	untimed.AttemptedAt = time.Time{}
	untimed.ID = storekit.Get[types.WebhookDelivery](t)(ws.AddDelivery(ctx, untimed)).ID
	storekit.Same(t, "untimed last", []types.WebhookDelivery{b2, a1, untimed},
		deliveries(store.DeliveryFilter{})[3:])
	storekit.Same(t, "untimed outside windows", []types.WebhookDelivery{b2},
		deliveries(store.DeliveryFilter{To: storekit.At(3), WebhookKey: "b"}))

	if n := storekit.Get[int64](t)(ws.PruneDeliveries(ctx, storekit.At(3))); n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	storekit.Same(
		t,
		"after prune",
		[]types.WebhookDelivery{b9, a3b, a3, untimed},
		deliveries(store.DeliveryFilter{}),
	)
	storekit.Must(t, ws.Delete(ctx, "a"))
	storekit.Same(
		t,
		"after webhook delete",
		[]types.WebhookDelivery{b9, untimed},
		deliveries(store.DeliveryFilter{}),
	)
}
