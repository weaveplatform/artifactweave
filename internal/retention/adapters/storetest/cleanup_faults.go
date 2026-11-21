package storetest

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// CleanupFaults injects an error after a real store operation. In particular,
// write faults test rollback of effects that already reached the backend,
// rather than substituting a fake store that never performed the mutation.
type CleanupFaults struct {
	port.CleanupStore
	After func(context.Context, string) error
}

func (s CleanupFaults) result(ctx context.Context, operation string, err error) error {
	if err == nil && s.After != nil {
		err = s.After(ctx, operation)
	}
	if err != nil {
		return fmt.Errorf("cleanup fault boundary %s: %w", operation, err)
	}
	return nil
}

// CreatePolicy applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) CreatePolicy(ctx context.Context, p model.CleanupPolicy) error {
	return s.result(ctx, "CreatePolicy", s.CleanupStore.CreatePolicy(ctx, p))
}

// Policy applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Policy(ctx context.Context, key string) (model.CleanupPolicy, error) {
	p, err := s.CleanupStore.Policy(ctx, key)
	return p, s.result(ctx, "Policy", err)
}

// UpdatePolicy applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) UpdatePolicy(ctx context.Context, p model.CleanupPolicy) error {
	return s.result(ctx, "UpdatePolicy", s.CleanupStore.UpdatePolicy(ctx, p))
}

// DeletePolicy applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) DeletePolicy(ctx context.Context, key string) error {
	return s.result(ctx, "DeletePolicy", s.CleanupStore.DeletePolicy(ctx, key))
}

// Policies applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Policies(ctx context.Context, q port.PolicyFilter) ([]model.CleanupPolicy, error) {
	rows, err := s.CleanupStore.Policies(ctx, q)
	return rows, s.result(ctx, "Policies", err)
}

// CreateRun applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) CreateRun(ctx context.Context, r model.CleanupRun) (model.CleanupRun, error) {
	r, err := s.CleanupStore.CreateRun(ctx, r)
	return r, s.result(ctx, "CreateRun", err)
}

// Run applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Run(ctx context.Context, id int64) (model.CleanupRun, error) {
	r, err := s.CleanupStore.Run(ctx, id)
	return r, s.result(ctx, "Run", err)
}

// UpdateRun applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) UpdateRun(ctx context.Context, r model.CleanupRun) error {
	return s.result(ctx, "UpdateRun", s.CleanupStore.UpdateRun(ctx, r))
}

// Runs applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Runs(ctx context.Context, q port.RunFilter, page kernel.Page) ([]model.CleanupRun, string, error) {
	rows, next, err := s.CleanupStore.Runs(ctx, q, page)
	return rows, next, s.result(ctx, "Runs", err)
}

// LastRun applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) LastRun(ctx context.Context, key string) (model.CleanupRun, error) {
	r, err := s.CleanupStore.LastRun(ctx, key)
	return r, s.result(ctx, "LastRun", err)
}

// CreateExecution applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) CreateExecution(ctx context.Context, e model.CleanupExecution) error {
	return s.result(ctx, "CreateExecution", s.CleanupStore.CreateExecution(ctx, e))
}

// Execution applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Execution(ctx context.Context, id int64) (model.CleanupExecution, error) {
	e, err := s.CleanupStore.Execution(ctx, id)
	return e, s.result(ctx, "Execution", err)
}

// ExecutionForOccurrence applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) ExecutionForOccurrence(ctx context.Context, key string) (model.CleanupExecution, error) {
	e, err := s.CleanupStore.ExecutionForOccurrence(ctx, key)
	return e, s.result(ctx, "ExecutionForOccurrence", err)
}

// CreateProgress applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) CreateProgress(ctx context.Context, p model.CleanupProgress) error {
	return s.result(ctx, "CreateProgress", s.CleanupStore.CreateProgress(ctx, p))
}

// Progress applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Progress(ctx context.Context, id int64) (model.CleanupProgress, error) {
	p, err := s.CleanupStore.Progress(ctx, id)
	return p, s.result(ctx, "Progress", err)
}

// AdvanceProgress applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) AdvanceProgress(ctx context.Context, revision int64, p model.CleanupProgress) error {
	return s.result(ctx, "AdvanceProgress", s.CleanupStore.AdvanceProgress(ctx, revision, p))
}

// CreateEntry applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) CreateEntry(ctx context.Context, e model.CleanupEntry) (bool, error) {
	created, err := s.CleanupStore.CreateEntry(ctx, e)
	return created, s.result(ctx, "CreateEntry", err)
}

// Entry applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Entry(ctx context.Context, run int64, kind model.CleanupEntryKind, id int64) (model.CleanupEntry, error) {
	e, err := s.CleanupStore.Entry(ctx, run, kind, id)
	return e, s.result(ctx, "Entry", err)
}

// AdvanceEntry applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) AdvanceEntry(ctx context.Context, revision int64, e model.CleanupEntry) error {
	return s.result(ctx, "AdvanceEntry", s.CleanupStore.AdvanceEntry(ctx, revision, e))
}

// Entries applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) Entries(ctx context.Context, q port.CleanupEntryQuery, page kernel.Page) ([]model.CleanupEntry, string, error) {
	rows, next, err := s.CleanupStore.Entries(ctx, q, page)
	return rows, next, s.result(ctx, "Entries", err)
}

// PendingEntry applies the configured fault after the underlying operation succeeds.
func (s CleanupFaults) PendingEntry(ctx context.Context, run, version int64) (model.CleanupEntry, error) {
	e, err := s.CleanupStore.PendingEntry(ctx, run, version)
	return e, s.result(ctx, "PendingEntry", err)
}
