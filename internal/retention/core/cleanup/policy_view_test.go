package cleanup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitCleanupPolicyOverviewUsesRealScopedExecution(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "overview-alpha", input("overview-alpha", "alpha"))
	must(t, err)
	must(t, s.Enable(ctx, p.Key, true))
	r, err := s.ManualRun(ctx, p.Key, true)
	must(t, err)
	r.Status, r.Started, r.Ended, r.TotalItems = api.RunCompleted, s.Now(), s.Now(), 2
	must(t, db.Cleanup().UpdateRun(ctx, r))
	must(t, db.Tx(ctx, func(ctx context.Context) error { return db.Cleanup().MarkReportDownloaded(ctx, r.ID) }))
	_, err = db.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, ItemType: model.CleanupPackage, Status: api.RunCompleted, TotalItems: 999})
	must(t, err)
	build := input("future-build", "alpha")
	build.ItemType = model.CleanupBuild
	build.Enabled = true
	must(t, db.Cleanup().CreatePolicy(ctx, build))
	project := s
	project.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		return policyAuthority("alpha"), db.Projects().Guard(ctx)
	}
	views, err := project.PolicyViews(ctx, cleanup.PolicyViewQuery{ProjectKey: "alpha", ItemTypes: []model.CleanupItemType{model.CleanupPackage, model.CleanupBuild}})
	must(t, err)
	if len(views) != 2 {
		t.Fatal("overview lost matching policy definitions")
	}
	for _, view := range views {
		if view.Policy.ExecutionCredential != (kernel.CredentialReference{}) || view.Policy.ProjectIncarnation != "" {
			t.Fatal("overview exposed private execution binding")
		}
		if view.Policy.Key == p.Key {
			if view.Last == nil || view.Last.Run.ID != r.ID || !view.Last.Run.ReportDownloaded || !view.Next.Equal(s.Now().Add(time.Hour)) {
				t.Fatal("overview used placeholder history or lost schedule")
			}
		} else if view.Last != nil || !view.Next.IsZero() {
			t.Fatal("future policy family acquired fabricated package execution")
		}
	}
	views, err = project.PolicyViews(ctx, cleanup.PolicyViewQuery{ProjectKey: "alpha", PolicyKey: "unknown", ItemTypes: []model.CleanupItemType{model.CleanupPackage}})
	must(t, err)
	if len(views) != 0 {
		t.Fatal("policy filter returned another policy")
	}
	_, err = project.PolicyViews(ctx, cleanup.PolicyViewQuery{ProjectKey: "beta"})
	if !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("validation preceded current context authority", err)
	}
	for _, tc := range []struct {
		name  string
		kinds []model.CleanupItemType
	}{
		{"missing", nil},
		{"duplicate", []model.CleanupItemType{model.CleanupPackage, model.CleanupPackage}},
		{"unknown", []model.CleanupItemType{"unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := project.PolicyViews(ctx, cleanup.PolicyViewQuery{ProjectKey: "alpha", ItemTypes: tc.kinds})
			if !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("invalid overview item types accepted", err)
			}
		})
	}
}
