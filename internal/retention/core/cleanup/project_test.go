package cleanup_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitCleanupProjectRecreationCannotReuseAnOldPolicyOrExecution(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	s.PackageIndex = struct{ artifactsapi.PackageIndex }{}
	ctx := t.Context()
	_, err := s.Create(ctx, "lifetime-policy", input("lifetime-policy", "alpha"))
	must(t, err)
	must(t, s.Enable(ctx, "lifetime-policy", true))
	policy, err := s.Get(ctx, "lifetime-policy", false)
	must(t, err)
	preview, err := s.Select(ctx, policy.Key)
	must(t, err)
	first, err := s.ManualRun(ctx, policy.Key, false)
	must(t, err)
	original, err := db.Cleanup().Execution(ctx, first.ID)
	must(t, err)
	must(t, db.Projects().Delete(ctx, "alpha"))
	must(t, db.Projects().Create(ctx, accesstest.Project{Key: "alpha"}))
	current, err := db.Projects().Authority(ctx, "alpha")
	must(t, err)
	if original.ProjectIncarnation == string(current.Incarnation) {
		t.Fatal("recreated project reused an ended lifetime")
	}
	if _, err := s.ManualRun(ctx, policy.Key, false); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("platform administrator reused an old policy in a new project lifetime", err)
	}
	called := false
	admitted, err := s.WithPolicy(ctx, policy.Key, policy.Revision, func(context.Context, model.CleanupPolicy) error {
		called = true
		return nil
	})
	if admitted || called || !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("queued-generation admission ignored the project lifetime", err)
	}
	if _, _, err := preview.Repositories(ctx, kernel.Page{}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("old preview traversed a recreated project", err)
	}
	trigger := schedule.Trigger{
		Reference: schedule.Reference{Kind: cleanup.ScheduleKind, Key: policy.Key},
		Revision:  policy.Revision, ID: "old-project-fire", Fire: s.Now(), Fence: func(context.Context) error { return nil },
	}
	if _, err := s.ScheduledRun(ctx, trigger); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("old scheduled grant crossed the project lifetime fence", err)
	}
	// Verified re-enablement deliberately binds a new policy generation. It
	// cannot rewrite the original run's historical project or caller.
	first.Status = api.RunStopped
	must(t, db.Cleanup().UpdateRun(ctx, first))
	must(t, s.Enable(ctx, policy.Key, true))
	fresh, err := s.Get(ctx, policy.Key, false)
	must(t, err)
	if fresh.ProjectIncarnation != string(current.Incarnation) || fresh.Revision == policy.Revision {
		t.Fatal("verified re-enablement did not bind the new project generation")
	}
	second, err := s.ManualRun(ctx, policy.Key, false)
	must(t, err)
	latest, err := db.Cleanup().Execution(ctx, second.ID)
	must(t, err)
	if latest.ProjectIncarnation != string(current.Incarnation) {
		t.Fatal("new execution did not retain the current project lifetime")
	}
	retained, err := db.Cleanup().Execution(ctx, first.ID)
	must(t, err)
	if !reflect.DeepEqual(original, retained) {
		t.Fatal("project recreation rewrote admitted execution history")
	}
}
