package cleanup

import (
	"context"
	"fmt"
	"slices"

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
		var err error
		out, err = s.readRun(ctx, policyKey, trigger, authority)
		return err
	})
	if err != nil {
		return model.CleanupRunSnapshot{}, err
	}
	return out, nil
}

func (s Service) readRun(ctx context.Context, policyKey string, trigger int64, authority Policy) (model.CleanupRunSnapshot, error) {
	execution, err := s.Store.Cleanup().Execution(ctx, trigger)
	if err != nil {
		return model.CleanupRunSnapshot{}, fmt.Errorf("read cleanup history scope: %w", err)
	}
	policy := execution.Policy
	if !authority.Allows(policy.ProjectKey) {
		return model.CleanupRunSnapshot{}, kernel.ErrForbidden
	}
	// Instance-wide authority can inspect retained history. A recreated
	// project's administrator does not inherit its predecessor's history.
	if !authority.Allows("") {
		policy.ProjectIncarnation = execution.ProjectIncarnation
		if err := s.currentProject(ctx, policy); err != nil {
			return model.CleanupRunSnapshot{}, fmt.Errorf("read cleanup history project: %w", err)
		}
	}
	run, err := s.Store.Cleanup().Run(ctx, trigger)
	if err != nil {
		return model.CleanupRunSnapshot{}, fmt.Errorf("read cleanup history run: %w", err)
	}
	sameRun := execution.RunID == trigger && run.ID == trigger && run.TriggerID == trigger
	samePolicy := policy.Key == policyKey && run.PolicyKey == policyKey
	packageRun := policy.ItemType == model.CleanupPackage && run.ItemType == model.CleanupPackage
	supportedStatus := run.Status.Terminal() || slices.Contains(activeStatuses, run.Status)
	if !sameRun || !samePolicy || !packageRun || !supportedStatus {
		return model.CleanupRunSnapshot{}, kernel.ErrNotFound
	}
	return model.CleanupSnapshot(run, policy), nil
}

// RunReport combines a currently authorized history snapshot and its exact
// item outcomes under one metadata transaction. Original run credentials and
// current artifact/blob contents are not needed to describe retained effects.
type RunReport struct {
	Snapshot model.CleanupRunSnapshot
	Totals   model.CleanupOutcomeTotals
}

// Report supplies summary and CSV readers from the same retained facts.
func (s Service) Report(ctx context.Context, policyKey string, trigger int64) (RunReport, error) {
	if policyKey == "" || trigger <= 0 {
		return RunReport{}, fmt.Errorf("a policy key and positive trigger are required: %w", kernel.ErrInvalid)
	}
	var out RunReport
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		history, err := s.readRun(ctx, policyKey, trigger, authority)
		if err != nil {
			return err
		}
		totals, err := s.Store.Cleanup().OutcomeTotals(ctx, trigger)
		if err != nil {
			return fmt.Errorf("read cleanup report outcomes: %w", err)
		}
		if history.Run.CleanedItems != totals.Deleted || history.Run.CleanedSize != totals.DeletedVirtualBytes {
			return fmt.Errorf("cleanup report counters disagree with acknowledged effects: %w", kernel.ErrStore)
		}
		out = RunReport{Snapshot: history, Totals: totals}
		return nil
	})
	if err != nil {
		return RunReport{}, err
	}
	return out, nil
}
