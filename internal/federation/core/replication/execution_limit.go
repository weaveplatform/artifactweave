package replication

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
)

// ErrSelectionLimit stops admission before a new file intent or effect. It is
// not a successful end-of-catalogue signal: reconciliation must not follow a
// truncated source walk, and public completion must retain the closed refusal.
var ErrSelectionLimit = errors.New("replication: user query result limit reached")

func (s Service) allowExecutionSelection(ctx context.Context, run model.ExecutionRun, progress port.ExecutionProgressStore, path string) error {
	if run.Binding.SelectionLimit == 0 && s.UserQueryLimit == 0 {
		return nil
	}
	return s.run(ctx, func(ctx context.Context, policy Policy) error {
		if _, err := s.repository(ctx, run.Binding.Repository.Key, policy); err != nil {
			return err
		}
		limit := run.Binding.SelectionLimit
		if !policy.Platform() && s.UserQueryLimit > 0 && (limit == 0 || int64(s.UserQueryLimit) < limit) {
			limit = int64(s.UserQueryLimit)
		}
		if limit == 0 {
			return nil
		}
		count, selected, err := progress.SelectedPaths(ctx, run.Reference, path)
		if err != nil {
			return fmt.Errorf("read bounded replication selection: %w", err)
		}
		if count > limit || count == limit && !selected {
			return ErrSelectionLimit
		}
		return nil
	})
}
