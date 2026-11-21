package cleanup_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitCleanupRunViewCountsOnlyAuthorizedHistory(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	create := func(key, project string, at time.Time) model.CleanupRun {
		t.Helper()
		_, err := s.Create(ctx, key, input(key, project))
		must(t, err)
		r, err := s.ManualRun(ctx, key, true)
		must(t, err)
		r.Started, r.Ended, r.Status, r.TotalItems = at, at.Add(time.Minute), api.RunCompleted, 1
		must(t, db.Cleanup().UpdateRun(ctx, r))
		must(t, s.Delete(ctx, key))
		return r
	}
	first := create("first", "alpha", s.Now().Add(-time.Hour))
	last := create("last", "alpha", s.Now())
	_ = create("foreign", "beta", s.Now().Add(time.Hour))
	global := create("global", "", s.Now())
	project := s
	project.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		return policyAuthority("alpha"), db.Projects().Guard(ctx)
	}
	page, err := project.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha", PageSize: 1})
	must(t, err)
	if page.Total != 2 || page.PageSize != 1 || page.CurrentPage != 1 || page.LastPage != 2 || len(page.Runs) != 1 || page.Runs[0].Run.ID != last.ID {
		t.Fatal("foreign row consumed the authorized page", page)
	}
	page, err = project.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha", Page: 2, PageSize: 1})
	must(t, err)
	if page.Total != 2 || len(page.Runs) != 1 || page.Runs[0].Run.ID != first.ID {
		t.Fatal("deleted policy history was lost from page", page)
	}
	page, err = s.Runs(ctx, cleanup.RunViewQuery{})
	must(t, err)
	if page.Total != 1 || page.Runs[0].Run.ID != global.ID {
		t.Fatal("global context included project rows", page)
	}
	_, err = project.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "beta", Page: -1})
	if !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("filter validation preceded current project authority", err)
	}
	_, err = project.Runs(ctx, cleanup.RunViewQuery{})
	if !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("project administrator opened global context", err)
	}
	page, err = project.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha", Projects: []string{"beta"}})
	must(t, err)
	if page.Total != 0 || len(page.Runs) != 0 {
		t.Fatal("additional project filter widened authority")
	}
	from, to := first.Started.UnixMilli(), last.Started.UnixMilli()
	page, err = project.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha", FromMillis: &from, ToMillis: &to})
	must(t, err)
	if page.Total != 1 || page.Runs[0].Run.ID != first.ID {
		t.Fatal("epoch-millisecond bounds were not inclusive/exclusive")
	}
	page, err = project.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha", PageSize: 900})
	must(t, err)
	if page.PageSize != 500 {
		t.Fatal("page size was not capped")
	}
	must(t, db.Projects().Delete(ctx, "alpha"))
	must(t, db.Projects().Create(ctx, accesstest.Project{Key: "alpha"}))
	page, err = project.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha"})
	must(t, err)
	if page.Total != 0 {
		t.Fatal("new project lifetime inherited old report totals")
	}
	page, err = s.Runs(ctx, cleanup.RunViewQuery{ProjectKey: "alpha"})
	must(t, err)
	if page.Total != 2 {
		t.Fatal("platform administrator lost historical project runs")
	}
}

func TestUnitCleanupRunViewRejectsInvalidAndOverflowedFilters(t *testing.T) {
	s, _ := world(t, "*")
	maxTime, minTime := int64(math.MaxInt64), int64(math.MinInt64)
	from, to := int64(20), int64(10)
	for _, tc := range []struct {
		name  string
		query cleanup.RunViewQuery
	}{
		{name: "negative page", query: cleanup.RunViewQuery{Page: -1}},
		{name: "negative page size", query: cleanup.RunViewQuery{PageSize: -1}},
		{name: "offset overflow", query: cleanup.RunViewQuery{Page: math.MaxInt, PageSize: 500}},
		{name: "unknown sort", query: cleanup.RunViewQuery{SortBy: "password"}},
		{name: "unknown direction", query: cleanup.RunViewQuery{SortOrder: "up"}},
		{name: "unknown item type", query: cleanup.RunViewQuery{ItemTypes: []model.CleanupItemType{"archive"}}},
		{name: "unknown status", query: cleanup.RunViewQuery{Statuses: []api.RunStatus{"READY"}}},
		{name: "from overflow", query: cleanup.RunViewQuery{FromMillis: &maxTime}},
		{name: "to underflow", query: cleanup.RunViewQuery{ToMillis: &minTime}},
		{name: "backwards time", query: cleanup.RunViewQuery{FromMillis: &from, ToMillis: &to}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := s.Runs(t.Context(), tc.query)
			if !errors.Is(err, kernel.ErrInvalid) || page.Total != 0 || len(page.Runs) != 0 {
				t.Fatal("invalid filter published a run page", err)
			}
		})
	}
}
