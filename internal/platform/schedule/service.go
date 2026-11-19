package schedule

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// WorkKind is separate from interval maintenance kinds. Each logical occurrence
// has its own key, so work coalescing cannot replace another occurrence.
const WorkKind = "schedule:run"

// Backend composes schedule positions and queued effects over one transaction.
type Backend interface {
	tx.Runner
	Schedules() Store
	WorkQueue() work.WorkQueue
}

// Service schedules bounded catch-up passes. Now is injected for deterministic
// calculations; nil uses UTC time.Now. Existing fixed-interval jobs are separate.
type Service struct {
	Store Backend
	Now   func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Configure changes the owner's authoritative snapshot. Call inside that
// owner's transaction after acquiring its guard. Identical settings preserve
// position/revision; a semantic edit gets a fresh revision and starts after now.
func (s *Service) Configure(ctx context.Context, ref Reference, cron, zone string, enabled bool) (State, error) {
	if err := ValidateReference(ref); err != nil {
		return State{}, err
	}
	expression, err := Parse(cron, zone)
	if err != nil {
		return State{}, err
	}
	var state State
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		previous, err := s.Store.Schedules().Get(ctx, ref)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("read schedule configuration: %w", err)
		}
		if err == nil && previous.Cron == expression.String() && previous.Zone == expression.Zone() && previous.Enabled == enabled {
			state = previous
			return nil
		}
		state = State{Reference: ref, Revision: rand.Text(), Cron: expression.String(), Zone: expression.Zone(), Enabled: enabled}
		if enabled {
			state.Next, err = expression.Next(s.now())
			if err != nil {
				return fmt.Errorf("activate schedule: %w", err)
			}
		}
		if err := s.Store.Schedules().Put(ctx, state); err != nil {
			return fmt.Errorf("configure schedule: %w", err)
		}
		return nil
	})
	if err != nil {
		return State{}, fmt.Errorf("schedule configuration: %w", err)
	}
	return state, nil
}

// Trigger records a stable logical occurrence. It carries references and a
// revision, never a copied policy body. ID is stable across retries/restarts.
type Trigger struct {
	Reference Reference `json:"schedule"`
	Revision  string    `json:"revision"`
	Fire      time.Time `json:"fire"`
	ID        string    `json:"id"`
}

func triggerFor(state State) Trigger {
	// A JSON tuple is unambiguous even when names contain delimiters.
	raw, _ := json.Marshal([]string{state.Kind, state.Key, state.Revision, state.Next.UTC().Format(time.RFC3339Nano)})
	digest := sha256.Sum256(raw)
	return Trigger{Reference: state.Reference, Revision: state.Revision, Fire: state.Next.UTC(), ID: hex.EncodeToString(digest[:])}
}

// Tick enqueues at most limit occurrences, one per returned due schedule. A
// missed fire is caught up from its logical time, in bounded subsequent passes;
// repeated ticks never invent a second run for an already advanced position.
func (s *Service) Tick(ctx context.Context, limit int) (int, error) {
	now := s.now()
	due, err := s.Store.Schedules().Due(ctx, now, limit)
	if err != nil {
		return 0, fmt.Errorf("find due schedules: %w", err)
	}
	count := 0
	for _, candidate := range due {
		queued := false
		err = s.Store.Tx(ctx, func(ctx context.Context) error {
			current, err := s.Store.Schedules().Get(ctx, candidate.Reference)
			if errors.Is(err, kernel.ErrNotFound) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("lock due schedule: %w", err)
			}
			if !current.Enabled || current.Revision != candidate.Revision || !current.Next.Equal(candidate.Next) || current.Next.IsZero() || current.Next.After(now) {
				return nil
			}
			expression, err := Parse(current.Cron, current.Zone)
			if err != nil {
				return fmt.Errorf("read due expression: %w", err)
			}
			next, err := expression.Next(current.Next)
			if err != nil && !errors.Is(err, ErrNoFuture) {
				return fmt.Errorf("advance logical fire: %w", err)
			}
			if err := s.Store.Schedules().Advance(ctx, current, next); err != nil {
				return fmt.Errorf("advance schedule position: %w", err)
			}
			trigger := triggerFor(current)
			payload, _ := json.Marshal(trigger)
			if err := s.Store.WorkQueue().Enqueue(ctx, WorkKind, trigger.ID, payload, now); err != nil {
				return fmt.Errorf("enqueue schedule occurrence: %w", err)
			}
			queued = true
			return nil
		})
		if errors.Is(err, kernel.ErrConflict) {
			continue
		}
		if err != nil {
			return count, fmt.Errorf("schedule tick: %w", err)
		}
		if queued {
			count++
		}
	}
	return count, nil
}

// Run polls durable positions until cancellation. Quartz determines the fire
// times; poll only bounds discovery latency. Transient failures are returned to
// the caller's supervisor, and a restart resumes the same persisted positions.
func (s *Service) Run(ctx context.Context, poll time.Duration, limit int) error {
	if poll <= 0 {
		return fmt.Errorf("schedule: polling interval must be positive: %w", kernel.ErrInvalid)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := s.Tick(ctx, limit); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("poll schedules: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Handler performs the owner's work after admission. It runs outside the short
// admission transaction, so network I/O cannot hold metadata locks. Owners
// revalidate their revision at later bounded publication points when necessary.
type Handler func(context.Context, Trigger) error

// Register binds the shared dispatcher to the existing durable queue, retaining
// its lease generations, backoff and shutdown behavior. Register before Run.
func (s *Service) Register(queue *work.Queue, handlers map[string]Handler) error {
	bound := maps.Clone(handlers)
	if err := queue.Handle(WorkKind, func(ctx context.Context, item work.WorkItem) error {
		return s.Execute(ctx, item, bound)
	}); err != nil {
		return fmt.Errorf("register schedule dispatcher: %w", err)
	}
	return nil
}

// Execute rechecks current enabled/revision and the queue lease generation.
// Disabled/deleted/replaced schedules finish stale work without executing it.
func (s *Service) Execute(ctx context.Context, item work.WorkItem, handlers map[string]Handler) error {
	var trigger Trigger
	if err := json.Unmarshal(item.Payload, &trigger); err != nil || item.Kind != WorkKind || trigger.ID != item.Key || trigger.Fire.IsZero() {
		return fmt.Errorf("invalid schedule occurrence: %w", work.ErrPermanent)
	}
	expected := State{Reference: trigger.Reference, Revision: trigger.Revision, Next: trigger.Fire}
	if ValidateReference(trigger.Reference) != nil || triggerFor(expected).ID != item.Key {
		return fmt.Errorf("invalid schedule occurrence identity: %w", work.ErrPermanent)
	}
	admitted := false
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		current, err := s.Store.Schedules().Get(ctx, trigger.Reference)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read scheduled worker configuration: %w", err)
		}
		if !current.Enabled || current.Revision != trigger.Revision || !current.Next.IsZero() && !trigger.Fire.Before(current.Next) {
			return nil
		}
		leased, err := s.Store.WorkQueue().Get(ctx, item.Kind, item.Key)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read scheduled worker lease: %w", err)
		}
		admitted = leased.Generation == item.Generation && leased.LeasedBy == item.LeasedBy && leased.State == work.WorkLeased && leased.LeaseUntil.After(s.now())
		return nil
	})
	if err != nil {
		return fmt.Errorf("admit scheduled work: %w", err)
	}
	if !admitted {
		return nil
	}
	run := handlers[trigger.Reference.Kind]
	if run == nil {
		return fmt.Errorf("schedule owner has no handler: %w", work.ErrPermanent)
	}
	if err := run(ctx, trigger); err != nil {
		return fmt.Errorf("scheduled work: %w", err)
	}
	return nil
}
