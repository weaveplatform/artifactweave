// Bridge (P9d): declared in migration/notifications/core/port; PRO-02 deletes it.

package store

import ntfport "github.com/weaveplatform/artifactweave/migration/notifications/core/port"

type (
	WebhookFilter  = ntfport.WebhookFilter
	DeliveryFilter = ntfport.DeliveryFilter
	WebhookStore   = ntfport.WebhookStore
)
