package outbox

import (
	"context"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Actor is an explicit allowlist for durable module payloads. Credentials,
// token identifiers, scopes, groups and principal internals never enter it.
type Actor struct {
	Name      string `json:"name,omitempty"`
	Anonymous bool   `json:"anonymous,omitempty"`
	System    bool   `json:"system,omitempty"`
}

// ActorFrom captures only public caller identity. A service's explicit by-name
// is used without request context; unnamed maintenance is identified as system.
func ActorFrom(ctx context.Context, by string) Actor {
	if p, ok := kernel.FromContext(ctx); ok {
		return Actor{Name: p.Name, Anonymous: p.Anonymous}
	}
	if by != "" {
		return Actor{Name: by}
	}
	return Actor{System: true}
}

// Stage snapshots a validated event for append after domain writes finish,
// inside the owning transaction. Append failure still rolls back business
// state. The callback only takes the stream lock; it reads no mutable domain
// state. Unsupported/nontransactional contexts fail instead of emitting early.
func Stage(ctx context.Context, store EventStore, event Event) error {
	if err := ValidateEvent(event); err != nil {
		return fmt.Errorf("stage module event: %w", err)
	}
	event.Payload = slices.Clone(event.Payload)
	if err := tx.BeforeCommit(ctx, func(ctx context.Context) error {
		if _, err := store.AppendEvent(ctx, event); err != nil {
			return fmt.Errorf("append committed module transition: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("register module event finalizer: %w", err)
	}
	return nil
}
