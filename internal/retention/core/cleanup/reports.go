package cleanup

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// Run reads one exact trigger under current administration authority. Scope
// comes from the immutable execution, never a policy that may have been deleted
// or recreated under the same key. Original execution credentials grant no
// report access; the current caller must be authorized independently.
func (s Service) Run(ctx context.Context, policyKey string, trigger int64) (model.CleanupRunSnapshot, error) {
	if policyKey == "" || trigger <= 0 {
		return model.CleanupRunSnapshot{}, fmt.Errorf("a policy key and positive trigger are required: %w", kernel.ErrInvalid)
	}
	var out model.CleanupRunSnapshot
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		execution, err := s.Store.Cleanup().Execution(ctx, trigger)
		if err != nil {
			return fmt.Errorf("read cleanup history scope: %w", err)
		}
		policy := execution.Policy
		if !authority.Allows(policy.ProjectKey) {
			return kernel.ErrForbidden
		}
		// A current platform administrator can inspect instance history. A
		// later administrator of a recreated project does not inherit history
		// from its old lifetime merely by acquiring the same project key.
		if !authority.Allows("") {
			policy.ProjectIncarnation = execution.ProjectIncarnation
			if err := s.currentProject(ctx, policy); err != nil {
				return fmt.Errorf("read cleanup history project: %w", err)
			}
		}
		run, err := s.Store.Cleanup().Run(ctx, trigger)
		if err != nil {
			return fmt.Errorf("read cleanup history run: %w", err)
		}
		sameRun := execution.RunID == trigger && run.ID == trigger && run.TriggerID == trigger
		samePolicy := policy.Key == policyKey && run.PolicyKey == policyKey
		packageRun := policy.ItemType == model.CleanupPackage && run.ItemType == model.CleanupPackage
		if !sameRun || !samePolicy || !packageRun {
			return kernel.ErrNotFound
		}
		policy.ExecutionCredential, policy.ProjectIncarnation = kernel.CredentialReference{}, ""
		out = model.CleanupRunSnapshot{Run: run, Policy: policy}
		return nil
	})
	if err != nil {
		return model.CleanupRunSnapshot{}, err
	}
	return out, nil
}
