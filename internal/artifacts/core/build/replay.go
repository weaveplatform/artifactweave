package build

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// AppendKeyed is a keyed append's effect, which the replay runs inside its
// transaction (decision 0043, "Optional protected build append"). It locks
// the run the request selects before anything else, so the receipt binds the
// append to that run even when a newer run of the same number appears later,
// appends to it with the access current gives, which the caller evaluates
// from the credential as it stands now rather than the evaluator cached at
// login, and returns the run's ID for the receipt. Only metadata mutations
// and the existing immutable document-blob write run here; no remote work
// is dispatched from the transaction.
func (s *Service) AppendKeyed(
	ctx context.Context,
	ap Append,
	current func(context.Context) (Access, error),
) (int64, error) {
	if current == nil {
		return 0, ErrForbidden
	}
	b, err := s.Store.Builds().
		GetForUpdate(ctx, ProjectKey(ap.ProjectKey), ap.Name, ap.Number, kernel.Normalise(ap.Started))
	if err != nil {
		return 0, fmt.Errorf("build: select append target: %w", err)
	}
	a, err := current(ctx)
	if err != nil {
		return 0, err
	}
	selected := ap
	selected.Started = b.Started
	if err := s.Append(ctx, selected, a); err != nil {
		return 0, err
	}
	return b.ID, nil
}

// ConfirmAppend is a keyed append's reload: it locks the run id names, which
// the receipt holds, and confirms that it is still the run the request
// addresses and that the credential, as current gives it now, may still
// append to it. A run deleted since answers ErrNotFound, one renamed or
// moved ErrConflict, and revoked access ErrForbidden, so that a replay never
// outlives the access that made it.
func (s *Service) ConfirmAppend(
	ctx context.Context,
	ap Append,
	id int64,
	current func(context.Context) (Access, error),
) error {
	if current == nil {
		return ErrForbidden
	}
	started := kernel.Normalise(ap.Started)
	b, err := s.Store.Builds().ByIDForUpdate(ctx, id)
	if err != nil {
		return fmt.Errorf("build: original append target: %w", err)
	}
	if b.ProjectKey != ProjectKey(ap.ProjectKey) || b.Name != ap.Name ||
		b.Number != ap.Number ||
		!started.IsZero() && !b.Started.Equal(started) {
		return fmt.Errorf("build: original append target changed: %w", ErrConflict)
	}
	c, err := s.repository(ctx, b.ProjectKey)
	if err != nil {
		return err
	}
	a, err := current(ctx)
	if err != nil {
		return err
	}
	_, path := b.DocumentItem()
	if !a.Build(c.Key, path, false, api.ActionWrite|api.ActionDelete) {
		return ErrForbidden
	}
	return nil
}
