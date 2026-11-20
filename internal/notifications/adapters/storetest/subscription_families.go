package storetest

import (
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	webhooksvc "github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunSubscriptionFamilies covers the twelve domain families supplied by the
// provider's 24 standard/custom resources, plus later-domain configuration echo.
// It makes no assertion that an event producer or delivery engine exists.
func RunSubscriptionFamilies(t *testing.T, newStore Factory) {
	t.Helper()
	families := []struct {
		domain string
		events []string
	}{
		{"artifact", []string{"deployed", "deleted", "moved", "copied", "cached"}},
		{"artifact_property", []string{"added", "deleted"}},
		{"docker", []string{"pushed", "deleted", "promoted"}},
		{"build", []string{"uploaded", "deleted", "promoted"}},
		{"user", []string{"locked"}},
		{"release_bundle", []string{"created", "signed", "deleted"}},
		{"distribution", []string{"distribute_started", "distribute_completed", "distribute_aborted", "distribute_failed", "delete_started", "delete_completed", "delete_failed"}},
		{"artifactory_release_bundle", []string{"received", "delete_started", "delete_completed", "delete_failed"}},
		{"destination", []string{"received", "delete_started", "delete_completed", "delete_failed"}},
		{"release_bundle_v2", []string{"release_bundle_v2_started", "release_bundle_v2_failed", "release_bundle_v2_completed"}},
		{"release_bundle_v2_promotion", []string{"release_bundle_v2_promotion_started", "release_bundle_v2_promotion_failed", "release_bundle_v2_promotion_completed"}},
		{"artifact_lifecycle", []string{"archive", "restore"}},
		{"xray_scan_status", []string{"done", "failed", "partial", "not_supported"}},
		{"app_trust", []string{"future-gate-name"}},
		{"curation", []string{"future-policy-name"}},
	}
	st, ctx := newStore(t), t.Context()
	svc := webhooksvc.Service{Store: st}
	for _, family := range families {
		for _, handler := range []model.HandlerType{model.HandlerWebhook, model.HandlerCustomWebhook} {
			t.Run(family.domain+"/"+string(handler), func(t *testing.T) {
				in := webhooksvc.Input{
					Key: family.domain + "-" + string(handler), Enabled: true,
					Domain: model.WebhookDomain(family.domain), EventTypes: family.events,
					Criteria: model.WebhookCriteria{
						AnyLocal: true, AnyBuild: true, AnyReleaseBundle: true,
						IncludePatterns: []string{"**"}, SelectedReleaseBundles: map[string][]string{"bundle": {"1"}},
						Extra: map[string]any{"futureCondition": map[string]any{"nested": []any{"unchanged", true}}},
					},
					Handlers: []webhooksvc.HandlerInput{{Type: handler, URL: "https://receiver.example/hook"}},
				}
				storekit.Must(t, (webhooksvc.Validator{}).Validate(ctx, in))
				created := storekit.Get[webhooksvc.View](t)(svc.Create(ctx, in))
				read := storekit.Get[webhooksvc.View](t)(svc.Get(ctx, in.Key))
				if !reflect.DeepEqual(created, read) || !reflect.DeepEqual(read.EventTypes, family.events) ||
					!reflect.DeepEqual(read.Criteria, in.Criteria) || read.Handlers[0].Type != handler {
					t.Fatalf("configuration changed after persistence: %+v", read)
				}
			})
		}
	}
	if list := storekit.Get[[]webhooksvc.View](t)(svc.List(ctx, port.WebhookFilter{})); len(list) != 30 {
		t.Fatalf("stored %d configurations, want 24 provider resources and 6 later-domain variants", len(list))
	}
}
