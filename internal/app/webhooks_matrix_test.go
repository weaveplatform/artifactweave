package app

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
)

// The common matrix has no project administrators or Manage Webhooks users;
// test/server/notifications adds those current-role and scope-specific cases.
func webhookMatrix() []matrixRow {
	const base = "/event/api/v1/subscriptions"
	const member = base + "/{key}"
	const body = `{"key":"created-web-{p}","enabled":true,"event_filter":{"domain":"user","event_types":["locked"]},"handlers":[{"handler_type":"webhook","url":"https://receiver.example/hook"}]}`
	const update = `{"enabled":false,"event_filter":{"domain":"user","event_types":["locked"]},"handlers":[{"handler_type":"webhook","url":"https://receiver.example/updated"}]}`
	seed := func(t *testing.T, f *matrixFixture, principal string) {
		t.Helper()
		if err := f.db.Webhooks().Create(t.Context(), model.WebhookSubscription{
			Key: "web-" + principal, Domain: model.DomainUser, EventTypes: []string{"locked"},
			Handlers: []model.WebhookHandler{{Type: model.HandlerWebhook, URL: "https://receiver.example/hook"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return []matrixRow{
		at("GET "+base, "", administrators, 200),
		at("POST "+base, body, administrators, 201),
		row("GET "+member, base+"/web-{p}", "", administrators, 200).seeding(seed),
		row("PUT "+member, base+"/web-{p}", update, administrators, 204).seeding(seed),
		row("DELETE "+member, base+"/web-{p}", "", administrators, 204).seeding(seed),
	}
}
