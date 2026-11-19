package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Reference identifies an owner's schedule without importing its module.
type Reference struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// State is the authoritative scheduling snapshot, updated in the owner's
// transaction. Revision changes on edits, enablement changes and recreation.
// Next is the next unqueued logical occurrence; zero means disabled/exhausted.
type State struct {
	Reference
	Revision string
	Cron     string
	Zone     string
	Enabled  bool
	Next     time.Time
}

// Store keeps positions after queue items complete and disappear. Every call
// joins the shared unit of work. Lock owner/domain state before schedule state,
// and schedule state before queue rows. Never read an owner under its schedule
// lock; keep this snapshot current in that owner's transaction instead.
type Store interface {
	Put(context.Context, State) error
	// Get locks the row when called inside a transaction.
	Get(context.Context, Reference) (State, error)
	Delete(context.Context, Reference) error
	// Due is bounded and ordered by (next_fire,kind,key), without write locks.
	Due(context.Context, time.Time, int) ([]State, error)
	// Advance requires a transaction and current enabled/revision/fire equality.
	// A stale position returns kernel.ErrConflict. next must advance or be zero.
	Advance(context.Context, State, time.Time) error
}

// ValidateReference bounds names for work identity and portable text storage.
func ValidateReference(r Reference) error {
	if r.Kind == "" || len(r.Kind) > work.MaxWorkKind || r.Key == "" || len(r.Key) > work.MaxWorkKey ||
		!utf8.ValidString(r.Kind) || !utf8.ValidString(r.Key) || strings.ContainsRune(r.Kind, 0) || strings.ContainsRune(r.Key, 0) {
		return fmt.Errorf("schedule: invalid reference: %w", kernel.ErrInvalid)
	}
	return nil
}

// ValidateState refuses malformed configuration before it becomes active.
func ValidateState(s State) error {
	if err := ValidateReference(s.Reference); err != nil {
		return err
	}
	if s.Revision == "" || len(s.Revision) > 128 || !utf8.ValidString(s.Revision) || strings.ContainsRune(s.Revision, 0) ||
		!s.Enabled && !s.Next.IsZero() || s.Next.Nanosecond() != 0 {
		return fmt.Errorf("schedule: invalid state: %w", kernel.ErrInvalid)
	}
	if _, err := Parse(s.Cron, s.Zone); err != nil {
		return err
	}
	return nil
}

// ValidateAdvance checks a proposed position change; the store also checks
// current persisted equality under its transaction guard.
func ValidateAdvance(expected State, next time.Time) error {
	if err := ValidateState(expected); err != nil {
		return err
	}
	if !expected.Enabled || expected.Next.IsZero() || next.Nanosecond() != 0 || !next.IsZero() && !next.After(expected.Next) {
		return fmt.Errorf("schedule: position must advance: %w", kernel.ErrInvalid)
	}
	return nil
}
