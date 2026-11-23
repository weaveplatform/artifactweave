package retention_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
)

// An incompletely composed module refuses to execute, register work or
// restore a stored caller instead of running cleanup without its contracts.
func TestUnitRetentionRefusesIncompleteExecutionComposition(t *testing.T) {
	m, err := retention.New(retention.Deps{Store: inmem.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Executor(); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("executor without removal, index or access contracts: %v", err)
	}
	if err := m.RegisterWork(nil); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("registration without a queue: %v", err)
	}
	if _, err := m.Policies().Restore(t.Context(), kernel.CredentialReference{}); err == nil {
		t.Fatal("an empty stored caller reference was restored")
	}
	handler := m.ScheduleHandlers()[cleanup.ScheduleKind]
	if handler == nil {
		t.Fatal("package cleanup schedule handler is missing")
	}
	err = handler(t.Context(), schedule.Trigger{
		Reference: schedule.Reference{Kind: cleanup.ScheduleKind, Key: "missing"},
		Fence:     func(context.Context) error { return nil },
	})
	if err == nil {
		t.Fatal("a schedule occurrence for a missing policy was admitted")
	}
}
