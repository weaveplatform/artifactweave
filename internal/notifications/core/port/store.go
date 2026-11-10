package port

import "github.com/weaveplatform/artifactweave/internal/platform/tx"

// Store is the part of the metadata store the notifications module uses:
// its webhook subscriptions and deliveries, with the shared unit of work
// (tx.Runner).
type Store interface {
	tx.Runner
	Webhooks() WebhookStore
}
