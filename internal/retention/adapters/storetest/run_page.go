package storetest

import (
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func testCleanupRunPages(t *testing.T, store port.Store) {
	t.Helper()
	ctx, cs := t.Context(), store.Cleanup()
	create := func(key, project, incarnation string, r model.CleanupRun) model.CleanupRun {
		t.Helper()
		p := cleanupPolicy(key, model.CleanupPackage, project)
		storekit.Must(t, cs.CreatePolicy(ctx, p))
		r.PolicyKey, r.ItemType = key, model.CleanupPackage
		if !r.Started.IsZero() {
			r.Ended = r.Started.Add(time.Minute)
		}
		r = storekit.Get[model.CleanupRun](t)(cs.CreateRun(ctx, r))
		r.TriggerID = r.ID
		storekit.Must(t, cs.UpdateRun(ctx, r))
		storekit.Must(
			t,
			cs.CreateExecution(ctx, model.CleanupExecution{RunID: r.ID, Policy: p, Credential: p.ExecutionCredential, ProjectIncarnation: incarnation, At: storekit.At(5)}),
		)
		storekit.Must(t, cs.DeletePolicy(ctx, key))
		return r
	}
	g := create(
		"r-g",
		"",
		"",
		model.CleanupRun{
			Started:      storekit.At(1),
			TotalItems:   10,
			CleanedItems: 1,
			CleanedSize:  100,
			Status:       api.RunCompleted,
			TriggeredBy:  "Z",
			Report:       []byte("private legacy report"),
		},
	)
	a := create(
		"r-a",
		"alpha",
		"old-alpha",
		model.CleanupRun{Started: storekit.At(2), TotalItems: 30, CleanedItems: 3, CleanedSize: 300, Status: api.RunFailed, TriggeredBy: "a", Manual: true},
	)
	_ = create("r-b", "beta", "beta", model.CleanupRun{Started: storekit.At(3), TotalItems: 99, CleanedItems: 99, Status: api.RunCompleted})
	c := create("r-c", "alpha", "new-alpha", model.CleanupRun{Started: storekit.At(1), TotalItems: 40, Status: api.RunCompleted, TriggeredBy: "z", DryRun: true})
	d := create("r-d", "alpha", "new-alpha", model.CleanupRun{Status: api.RunInProgress, TriggeredBy: "ä", Manual: true})
	e := create("r-e", "", "", model.CleanupRun{Started: storekit.At(-1), TotalItems: 20, Status: api.RunStopped, TriggeredBy: "A", Manual: true, DryRun: true})
	_ = create("r-warm", "", "", model.CleanupRun{Status: api.RunWarmed, TotalItems: 100})
	mismatch := create("r-mismatch", "", "", model.CleanupRun{Status: api.RunCompleted, TotalItems: 100})
	mismatch.TriggerID++
	storekit.Must(t, cs.UpdateRun(ctx, mismatch))
	_, err := cs.CreateRun(ctx, model.CleanupRun{PolicyKey: "unbound", ItemType: model.CleanupPackage, Status: api.RunCompleted, TotalItems: 100})
	storekit.Must(t, err)
	_, err = cs.CreateRun(ctx, model.CleanupRun{PolicyKey: "build", ItemType: model.CleanupBuild, Status: api.RunCompleted, TotalItems: 100})
	storekit.Must(t, err)
	// A new owner of the reused policy key must not change historical visibility.
	storekit.Must(t, cs.CreatePolicy(ctx, cleanupPolicy(c.PolicyKey, model.CleanupPackage, "beta")))
	all := []port.RunScope{{ProjectKey: ""}, {ProjectKey: "alpha"}}
	base := port.RunListing{Scopes: all, Limit: 500, Descending: true}
	ids := func(page port.RunPage) []int64 {
		out := make([]int64, 0, len(page.Runs))
		for _, snapshot := range page.Runs {
			if len(snapshot.Run.Report) != 0 {
				t.Fatal("run page hydrated a private report blob")
			}
			if snapshot.Policy.ExecutionCredential != (kernel.CredentialReference{}) || snapshot.Policy.ProjectIncarnation != "" {
				t.Fatal("view exposed private execution binding")
			}
			out = append(out, snapshot.Run.ID)
		}
		return out
	}
	from, to := storekit.At(1).UnixMicro(), storekit.At(2).UnixMicro()
	for _, tc := range []struct {
		name   string
		change func(*port.RunListing)
		want   []int64
		total  int64
	}{
		{name: "scope before page", want: []int64{a.ID, c.ID, g.ID, e.ID, d.ID}, total: 5},
		{name: "global only", change: func(q *port.RunListing) { q.Scopes = []port.RunScope{{ProjectKey: ""}} }, want: []int64{g.ID, e.ID}, total: 2},
		{name: "current lifetime", change: func(q *port.RunListing) { q.Scopes = []port.RunScope{{ProjectKey: "alpha", Incarnation: "new-alpha"}} }, want: []int64{c.ID, d.ID}, total: 2},
		{name: "no scope", change: func(q *port.RunListing) { q.Scopes = nil }, want: []int64{}},
		{name: "unknown lifetime", change: func(q *port.RunListing) { q.Scopes = []port.RunScope{{ProjectKey: "alpha", Incarnation: "unknown"}} }, want: []int64{}},
		{name: "offset page", change: func(q *port.RunListing) { q.Offset, q.Limit = 2, 2 }, want: []int64{g.ID, e.ID}, total: 5},
		{name: "past end", change: func(q *port.RunListing) { q.Offset = 500 }, want: []int64{}, total: 5},
		{name: "exact policy", change: func(q *port.RunListing) { q.PolicyKey = c.PolicyKey }, want: []int64{c.ID}, total: 1},
		{name: "status", change: func(q *port.RunListing) { q.Statuses = []api.RunStatus{api.RunFailed} }, want: []int64{a.ID}, total: 1},
		{name: "from inclusive to exclusive", change: func(q *port.RunListing) { q.From, q.To = &from, &to }, want: []int64{c.ID, g.ID}, total: 2},
		{name: "only from", change: func(q *port.RunListing) { q.From = &from }, want: []int64{a.ID, c.ID, g.ID}, total: 3},
		{name: "only to", change: func(q *port.RunListing) { q.To = &to }, want: []int64{c.ID, g.ID, e.ID}, total: 3},
		{name: "exclude blank", change: func(q *port.RunListing) { q.ExcludeBlank = true }, want: []int64{a.ID, c.ID, g.ID, e.ID}, total: 4},
		{name: "future item type", change: func(q *port.RunListing) { q.ItemTypes = []model.CleanupItemType{model.CleanupBuild} }, want: []int64{}},
		{name: "package among requested types", change: func(q *port.RunListing) {
			q.ItemTypes = []model.CleanupItemType{model.CleanupBuild, model.CleanupPackage}
		}, want: []int64{a.ID, c.ID, g.ID, e.ID, d.ID}, total: 5},
		{name: "warming never package work", change: func(q *port.RunListing) { q.Statuses = []api.RunStatus{api.RunWarmed} }, want: []int64{}},
		{name: "literal policy filter", change: func(q *port.RunListing) { q.PolicyKey = "r-c' OR 1=1 --" }, want: []int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			if tc.change != nil {
				tc.change(&q)
			}
			page := storekit.Get[port.RunPage](t)(cs.RunPage(ctx, q))
			storekit.Same(t, "visible run window", tc.want, ids(page))
			storekit.Same(t, "visible total", tc.total, page.Total)
		})
	}
	for _, tc := range []struct {
		by        port.RunOrder
		ascending []int64
	}{
		{by: port.RunOrderID, ascending: []int64{g.ID, a.ID, c.ID, d.ID, e.ID}},
		{by: port.RunOrderItemType, ascending: []int64{g.ID, a.ID, c.ID, d.ID, e.ID}},
		{by: port.RunOrderPolicy, ascending: []int64{a.ID, c.ID, d.ID, e.ID, g.ID}},
		{by: port.RunOrderProject, ascending: []int64{g.ID, e.ID, a.ID, c.ID, d.ID}},
		{by: port.RunOrderStarted, ascending: []int64{d.ID, e.ID, g.ID, c.ID, a.ID}},
		{by: port.RunOrderEnded, ascending: []int64{d.ID, e.ID, g.ID, c.ID, a.ID}},
		{by: port.RunOrderTotal, ascending: []int64{d.ID, g.ID, e.ID, a.ID, c.ID}},
		{by: port.RunOrderCleanedSize, ascending: []int64{c.ID, d.ID, e.ID, g.ID, a.ID}},
		{by: port.RunOrderCleanedItems, ascending: []int64{c.ID, d.ID, e.ID, g.ID, a.ID}},
		{by: port.RunOrderStatus, ascending: []int64{g.ID, c.ID, a.ID, d.ID, e.ID}},
		{by: port.RunOrderActor, ascending: []int64{e.ID, g.ID, a.ID, c.ID, d.ID}},
		{by: port.RunOrderManual, ascending: []int64{g.ID, c.ID, a.ID, d.ID, e.ID}},
		{by: port.RunOrderDry, ascending: []int64{g.ID, a.ID, d.ID, c.ID, e.ID}},
	} {
		t.Run("sort/"+string(tc.by), func(t *testing.T) {
			q := base
			q.Order, q.Descending = tc.by, false
			page := storekit.Get[port.RunPage](t)(cs.RunPage(ctx, q))
			storekit.Same(t, "ascending numeric ties and nulls", tc.ascending, ids(page))
			q.Descending = true
			page = storekit.Get[port.RunPage](t)(cs.RunPage(ctx, q))
			want := slices.Clone(tc.ascending)
			slices.Reverse(want)
			storekit.Same(t, "descending numeric ties and nulls", want, ids(page))
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*port.RunListing)
	}{
		{name: "negative offset", change: func(q *port.RunListing) { q.Offset = -1 }},
		{name: "empty window", change: func(q *port.RunListing) { q.Limit = 0 }},
		{name: "unbounded window", change: func(q *port.RunListing) { q.Limit = 501 }},
		{name: "untrusted sort", change: func(q *port.RunListing) { q.Order = "id; DELETE FROM cleanup_runs" }},
		{name: "backwards time", change: func(q *port.RunListing) { q.From, q.To = &to, &from }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			tc.change(&q)
			_, err := cs.RunPage(ctx, q)
			storekit.WantErr(t, err, kernel.ErrInvalid)
		})
	}
	page := storekit.Get[port.RunPage](t)(cs.RunPage(ctx, base))
	page.Runs[0].Policy.Criteria.Repos[0] = "changed-outside-the-store"
	again := storekit.Get[port.RunPage](t)(cs.RunPage(ctx, base))
	if again.Runs[0].Policy.Criteria.Repos[0] != "npm-local" {
		t.Fatal("run view returned mutable stored policy")
	}
}
