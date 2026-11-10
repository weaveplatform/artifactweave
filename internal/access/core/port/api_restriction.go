package port

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// APIRestriction is an administrator's temporary restriction of one managed
// user's authenticated API calls. A zero Until clears it. Changed and Actor
// describe the administrative change, not a client's request parameters.
type APIRestriction struct {
	User, Actor, Reason string
	Until, Changed      time.Time
	Sequence            int64
}

// Validate reports an error wrapping kernel.ErrInvalid unless r can be
// stored: User, Actor and Reason set, User and Actor at most 255 bytes and
// Reason at most 1024, Changed set, and a non-zero Until after Changed at
// the microsecond precision the stores keep. Both stores check it in Put
// before they write anything.
func (r APIRestriction) Validate() error {
	if r.User == "" || r.Actor == "" || r.Reason == "" || len(r.User) > 255 || len(r.Actor) > 255 ||
		len(
			r.Reason,
		) > 1024 || r.Changed.IsZero() || !r.Until.IsZero() && r.Until.UnixMicro() <= r.Changed.UnixMicro() {
		return fmt.Errorf("invalid API restriction: %w", kernel.ErrInvalid)
	}
	return nil
}

// APIRestrictionStore keeps current restrictions and a durable, append-only
// audit of changes. Deleting a user removes its current state but preserves the
// audit; recreating the name does not inherit the deleted user's restriction.
type APIRestrictionStore interface {
	Get(ctx context.Context, user string) (APIRestriction, error)
	// Put replaces current state and appends an audit row in one transaction.
	// The user must exist. The returned Sequence is assigned by the store.
	Put(ctx context.Context, restriction APIRestriction) (APIRestriction, error)
	// History pages newest first by Sequence; an empty user includes all users.
	History(ctx context.Context, user string, page kernel.Page) ([]APIRestriction, string, error)
}
