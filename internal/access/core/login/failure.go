package login

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Store joins the account mutation and its event in one metadata transaction.
type Store interface {
	tx.Runner
	Users() port.UserStore
	Instance() instance.InstanceStore
	Events() outbox.EventStore
}

// Failures records failed passwords at the service boundary. Callers can keep
// their established best-effort response only after this transaction returns.
type Failures struct {
	Store Store
	Now   func() time.Time
}

// RecordFailedLogin emits once for the enabled-to-locked transition. An append
// failure rolls back both the counter and status, retaining the existing refusal.
func (s Failures) RecordFailedLogin(ctx context.Context, name string, lockAt int) error {
	name = model.FoldUsername(name)
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		result, err := s.Store.Users().RecordFailedLoginResult(ctx, name, lockAt)
		if err != nil {
			return fmt.Errorf("login: record failure: %w", err)
		}
		if !result.NewlyLocked {
			return nil
		}
		origin, err := instance.EnsureID(ctx, s.Store)
		if err != nil {
			return fmt.Errorf("login: resolve event origin: %w", err)
		}
		facts := api.UserLockedEvent{Version: 1, Outcome: "committed", Actor: outbox.ActorFrom(ctx, ""), User: name}
		raw, err := json.Marshal(facts)
		if err != nil {
			return fmt.Errorf("login: encode event: %w", err)
		}
		now := time.Now().UTC()
		if s.Now != nil {
			if supplied := s.Now(); !supplied.IsZero() {
				now = supplied.UTC()
			}
		}
		if err := outbox.Stage(ctx, s.Store.Events(), outbox.Event{Origin: origin, Topic: api.UserLocked, Key: name, At: now, Payload: raw}); err != nil {
			return fmt.Errorf("login: stage event: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("login failure transaction: %w", err)
	}
	return nil
}
