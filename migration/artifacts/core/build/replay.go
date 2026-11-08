package build

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// AppendOnce commits the append and its receipt together. A replay stays bound
// to the original run even when a newer run of the same number exists. The
// caller supplies current authorization, not the evaluator cached at login.
// Only metadata mutations and the existing immutable document-blob write run
// here; no remote work is dispatched from the transaction.
func (s *Service) AppendOnce(
	ctx context.Context,
	ap Append,
	r store.ReplayRequest,
	current func(context.Context) (Access, error),
) (store.ReplayReceipt, error) {
	var receipt store.ReplayReceipt
	ap.Started = store.Normalise(ap.Started)
	if current == nil {
		return receipt, ErrForbidden
	}
	if _, err := s.Store.Replays().Prune(ctx, r.Now, 100); err != nil {
		return receipt, fmt.Errorf("build: prune replay receipts: %w", err)
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		receipt, err = s.Store.Replays().Do(ctx, r, func(ctx context.Context) (int64, error) {
			b, err := s.Store.Builds().
				GetForUpdate(ctx, ProjectKey(ap.ProjectKey), ap.Name, ap.Number, ap.Started)
			if err != nil {
				return 0, fmt.Errorf("build: select append target: %w", err)
			}
			a, err := current(ctx)
			if err != nil {
				return 0, err
			}
			selected := ap
			selected.Started = b.Started
			return b.ID, s.Append(ctx, selected, a)
		})
		if err != nil {
			return fmt.Errorf("build: protected append: %w", err)
		}
		b, err := s.Store.Builds().ByIDForUpdate(ctx, receipt.ResourceID)
		if err != nil {
			return fmt.Errorf("build: original append target: %w", err)
		}
		if b.ProjectKey != ProjectKey(ap.ProjectKey) || b.Name != ap.Name ||
			b.Number != ap.Number ||
			!ap.Started.IsZero() && !b.Started.Equal(ap.Started) {
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
		if !a.Build(c.Key, path, false, types.ActionWrite|types.ActionDelete) {
			return ErrForbidden
		}
		return nil
	})
	if err != nil {
		return receipt, fmt.Errorf("build: append once: %w", err)
	}
	return receipt, nil
}
