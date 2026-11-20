package events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// Delivery is one consumer's immutable event identity and source facts. Key
// identifies the consumer/origin/sequence tuple, so two events for the same
// artifact never coalesce into a single durable consumer handoff.
type Delivery struct {
	Consumer string
	Key      string
	Event    outbox.Event
}

// DeliveryKey is stable across retries, restarts and dispatch lease takeover.
func DeliveryKey(consumer string, event outbox.Event) string {
	// Encoding a tuple avoids delimiter collisions in otherwise valid keys.
	raw, _ := json.Marshal([3]any{consumer, event.Origin, event.Seq})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
