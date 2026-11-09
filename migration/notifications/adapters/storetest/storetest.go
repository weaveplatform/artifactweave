package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/notifications/core/port"
)

// Factory returns a fresh, empty notifications store and registers the
// store's cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the notifications contract on stores from newStore, one subtest
// per concern, each on a store of its own: Webhooks and WebhookDeliveries.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runWebhooks(t, newStore)
}
