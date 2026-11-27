package downloadadmission

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Begin persists admission before any response byte or member redirect. Calling
// done after the response finishes cannot be skipped by request cancellation.
func Begin(ctx context.Context, store any, configuration repo.Config, item model.Item) (done func(), err error) {
	// A local response can outlive migration to a federated repository. Its
	// resolved configuration can also be older than an already prepared barrier.
	if configuration.Class != repo.Local && configuration.Class != repo.Federated || item.Cached || item.Folder {
		return func() {}, nil
	}
	provider, ok := store.(interface{ CleanupExclusions() api.CleanupExclusions })
	if !ok || provider.CleanupExclusions() == nil {
		return nil, fmt.Errorf("hosted admission unavailable: %w", kernel.ErrStore)
	}
	exclusions := provider.CleanupExclusions()
	token, err := exclusions.Admit(ctx, item.RepoKey, item.ID)
	if err != nil {
		return nil, fmt.Errorf("hosted admission: %w", err)
	}
	done = func() {
		// A failed completion remains an active/unknown pin. No timer clears it.
		bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := exclusions.Complete(bounded, token); err != nil {
			slog.ErrorContext(bounded, "complete hosted download admission", "error", err)
		}
	}
	// The caller may already have opened and checked an immutable snapshot.
	// A same-ID overwrite must not reopen it or substitute different bytes.
	// Preparation drains every admission in the repository, independently of
	// current version links, so that selected snapshot remains protected.
	// Recheck identity and item kind without replacing the caller's snapshot.
	reader, ok := store.(interface{ Items() port.ItemStore })
	if !ok {
		done()
		return nil, kernel.ErrStore
	}
	current, err := reader.Items().GetByID(ctx, item.ID)
	if err != nil || current.RepoKey != item.RepoKey || current.Cached != item.Cached || current.Folder != item.Folder {
		done()
		if err != nil {
			return nil, fmt.Errorf("recheck admitted item: %w", err)
		}
		return nil, kernel.ErrConflict
	}
	return done, nil
}
