package cleanup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func TestUnitCleanupHistoryRetainsOriginalPolicyAndScope(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "historical-run", input("historical-run", "alpha"))
	must(t, err)
	r, err := s.ManualRun(ctx, p.Key, true)
	must(t, err)
	r.Status, r.Ended = api.RunCompleted, s.Now()
	r.Report = []byte("legacy private report payload")
	must(t, db.Cleanup().UpdateRun(ctx, r))
	changed := p
	changed.Description = "later configuration"
	_, err = s.Update(ctx, p.Key, changed)
	must(t, err)
	must(t, s.Delete(ctx, p.Key))
	_, err = s.Create(ctx, p.Key, input(p.Key, "beta"))
	must(t, err)
	alpha := s
	alpha.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		return policyAuthority("alpha"), db.Projects().Guard(ctx)
	}
	history, err := alpha.Run(ctx, p.Key, r.TriggerID)
	must(t, err)
	if history.Policy.Description != p.Description || history.Policy.ProjectKey != "alpha" || history.Run.ID != r.ID || history.Run.Status != api.RunCompleted {
		t.Fatal("policy reuse rewrote or hid the original run")
	}
	if history.Policy.ExecutionCredential != (kernel.CredentialReference{}) || history.Policy.ProjectIncarnation != "" {
		t.Fatal("history disclosed private execution bindings")
	}
	if len(history.Run.Report) != 0 {
		t.Fatal("bounded run metadata included a legacy report blob")
	}
	beta := s
	beta.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		return policyAuthority("beta"), db.Projects().Guard(ctx)
	}
	history, err = beta.Run(ctx, p.Key, r.TriggerID)
	if !errors.Is(err, kernel.ErrForbidden) || history.Run.ID != 0 || history.Policy.Key != "" {
		t.Fatal("new policy owner inherited the former project's report", err)
	}
	_, err = alpha.Run(ctx, "different-policy", r.TriggerID)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("history lookup substituted a policy key", err)
	}
	r.TriggerID++
	must(t, db.Cleanup().UpdateRun(ctx, r))
	_, err = alpha.Run(ctx, p.Key, r.ID)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("history accepted a mismatched trigger identity", err)
	}
}

type reportOutcomeFailure struct {
	port.CleanupStore
	called *bool
}

func (s reportOutcomeFailure) OutcomeTotals(ctx context.Context, id int64) (model.CleanupOutcomeTotals, error) {
	*s.called = true
	totals, err := s.CleanupStore.OutcomeTotals(ctx, id)
	if err != nil {
		return model.CleanupOutcomeTotals{}, err
	}
	return totals, errCleanupBoundary
}

func TestUnitCleanupReportsDistinguishSelectedAndCommittedEffects(t *testing.T) {
	for _, dry := range []bool{true, false} {
		name := "actual"
		if dry {
			name = "dry"
		}
		t.Run(name, func(t *testing.T) {
			s, db := world(t, "*")
			s.Work = db.WorkQueue()
			ctx := t.Context()
			p, err := s.Create(ctx, "report-effects", input("report-effects", "alpha"))
			must(t, err)
			if !dry {
				must(t, s.Enable(ctx, p.Key, true))
			}
			r, err := s.ManualRun(ctx, p.Key, dry)
			must(t, err)
			hash := strings.Repeat("a", 64)
			must(t, db.Tx(ctx, func(ctx context.Context) error {
				for id := int64(1); id <= 3; id++ {
					e := model.CleanupEntry{
						RunID: r.ID, EntityID: id, VersionID: 1, Revision: 1, Kind: model.CleanupItemEntry, State: model.CleanupSelected,
						Snapshot: artifactsapi.IndexFile{ID: id, Version: artifactsapi.IndexVersion{ID: 1}, Path: "item.bin", Size: 5, Actual: kernel.Checksums{SHA256: hash}},
					}
					if !dry {
						e.State = model.CleanupDeleted
						e.Removed = &artifactsapi.RemovedItem{ID: id, Size: 5, SHA256: hash}
					}
					if id == 3 {
						e.State, e.Removed = model.CleanupSkipped, nil
						if !dry {
							e.State = model.CleanupEntryFailed
						}
					}
					if _, err := db.Cleanup().CreateEntry(ctx, e); err != nil {
						return err
					}
				}
				return nil
			}))
			r.TotalItems, r.Started, r.Ended, r.Status = 3, s.Now(), s.Now(), api.RunCompleted
			if !dry {
				r.CleanedItems, r.CleanedSize, r.Status = 2, 10, api.RunCompletedWithErrors
			}
			must(t, db.Cleanup().UpdateRun(ctx, r))
			must(t, s.Delete(ctx, p.Key))
			report, err := s.Report(ctx, p.Key, r.ID)
			must(t, err)
			if report.Snapshot.Run.ID != r.ID || report.Snapshot.Run.DryRun != dry || report.Totals.Items != 3 {
				t.Fatal("report lost retained item outcomes")
			}
			if dry {
				if report.Totals.Selected != 2 || report.Totals.Skipped != 1 || report.Totals.Deleted != 0 || report.Totals.SelectedVirtualBytes != 10 ||
					report.Totals.SelectedContentBytes != 5 {
					t.Fatal("dry report invented deletion or double-counted content", report.Totals)
				}
			} else if report.Totals.Deleted != 2 || report.Totals.Failed != 1 || report.Totals.DeletedVirtualBytes != 10 || report.Totals.DeletedContentBytes != 5 {
				t.Fatal("actual report lost committed/error facts", report.Totals)
			}
			called := false
			broken := s
			broken.Store = cleanupBoundaryStore{Store: db, cleanup: reportOutcomeFailure{CleanupStore: db.Cleanup(), called: &called}}
			failed, err := broken.Report(ctx, p.Key, r.ID)
			if !called || !errors.Is(err, errCleanupBoundary) || failed.Snapshot.Run.ID != 0 || failed.Totals.Items != 0 {
				t.Fatal("failed aggregation returned partial report", err)
			}
			called = false
			broken.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
				return policyAuthority("beta"), db.Projects().Guard(ctx)
			}
			_, err = broken.Report(ctx, p.Key, r.ID)
			if called || !errors.Is(err, kernel.ErrForbidden) {
				t.Fatal("foreign report read aggregate facts before authorization", err)
			}
			r.CleanedItems++
			must(t, db.Cleanup().UpdateRun(ctx, r))
			failed, err = s.Report(ctx, p.Key, r.ID)
			if !errors.Is(err, kernel.ErrStore) || failed.Snapshot.Run.ID != 0 {
				t.Fatal("report accepted counters without matching effects", err)
			}
			r.Status = api.RunWarmed
			must(t, db.Cleanup().UpdateRun(ctx, r))
			_, err = s.Run(ctx, p.Key, r.ID)
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("warming history became package cleanup work", err)
			}
		})
	}
	if _, err := (cleanup.Service{}).Report(t.Context(), "p", 1); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("unavailable report service accepted history", err)
	}
	if _, err := (cleanup.Service{}).Report(t.Context(), "p", 0); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("invalid report trigger accepted", err)
	}
}

func TestUnitCleanupHistoryProjectRecreationRequiresCurrentScope(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "old-project-run", input("old-project-run", "alpha"))
	must(t, err)
	r, err := s.ManualRun(ctx, p.Key, true)
	must(t, err)
	must(t, db.Projects().Delete(ctx, "alpha"))
	must(t, db.Projects().Create(ctx, accesstest.Project{Key: "alpha"}))
	project := s
	project.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		return policyAuthority("alpha"), db.Projects().Guard(ctx)
	}
	history, err := project.Run(ctx, p.Key, r.ID)
	if !errors.Is(err, kernel.ErrConflict) || history.Run.ID != 0 {
		t.Fatal("new project lifetime inherited old history", err)
	}
	// Current instance-wide authority is independent of either project lifetime.
	history, err = s.Run(ctx, p.Key, r.ID)
	must(t, err)
	if history.Run.ID != r.ID {
		t.Fatal("platform administrator lost instance history")
	}
}

func TestUnitCleanupHistoryRejectsUnboundAndInvalidReferences(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		key  string
		id   int64
	}{{name: "missing policy", key: "", id: 1}, {name: "zero trigger", key: "policy", id: 0}, {name: "negative trigger", key: "policy", id: -1}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Run(ctx, tc.key, tc.id)
			if !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("malformed history reference was accepted", err)
			}
		})
	}
	legacy, err := db.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: "unbound-history", ItemType: model.CleanupPackage, Status: api.RunCompleted})
	must(t, err)
	_, err = s.Run(ctx, legacy.PolicyKey, legacy.ID)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("unbound legacy row acquired guessed global scope", err)
	}
}
