package cleanup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func TestUnitCleanupAdmissionFreezesProjectDisplayName(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "frozen-label", input("frozen-label", "alpha"))
	must(t, err)
	s.ProjectDisplayName = func(ctx context.Context, key string) (string, error) {
		if !db.InTx(ctx) || key != "alpha" {
			t.Fatal("display label was not read under guarded project admission")
		}
		return "Alpha team", nil
	}
	r, err := s.ManualRun(ctx, p.Key, true)
	must(t, err)
	r.Status, r.Ended = api.RunCompleted, s.Now()
	must(t, db.Cleanup().UpdateRun(ctx, r))
	s.ProjectDisplayName = func(context.Context, string) (string, error) { return "", errCleanupBoundary }
	view, err := s.Run(ctx, p.Key, r.ID)
	must(t, err)
	if view.DisplayProject != "Alpha team" {
		t.Fatal("history borrowed a later project label")
	}
	_, err = s.PolicyViews(ctx, cleanup.PolicyViewQuery{ProjectKey: "alpha", ItemTypes: []model.CleanupItemType{model.CleanupPackage}})
	if !errors.Is(err, errCleanupBoundary) {
		t.Fatal("configuration overview swallowed a current label read failure", err)
	}
}

func TestUnitCleanupDisplayReadFailureCannotAdmitRun(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "label-read-failure", input("label-read-failure", "alpha"))
	must(t, err)
	s.ProjectDisplayName = func(context.Context, string) (string, error) { return "partial label", errCleanupBoundary }
	r, err := s.ManualRun(ctx, p.Key, true)
	if r.ID != 0 || !errors.Is(err, errCleanupBoundary) {
		t.Fatal("label read failure published a partial admission", err)
	}
	rows, _, err := db.Cleanup().Runs(ctx, port.RunFilter{PolicyKey: p.Key}, kernel.Page{Limit: 10})
	must(t, err)
	if len(rows) != 0 {
		t.Fatal("failed label snapshot left a durable run")
	}
}
