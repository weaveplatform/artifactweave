package cleanup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
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
