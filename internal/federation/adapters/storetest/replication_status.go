package storetest

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func testReplicationStatusTargets(t *testing.T, store port.Store) {
	ctx, rs := t.Context(), store.Replications()
	for _, target := range []model.Replication{replication("k3", "other"), replication("k2", "selected"), replication("k1", "selected")} {
		storekit.Must(t, rs.Put(ctx, target))
	}
	first, next, err := rs.StatusTargets(ctx, "selected", kernel.Page{Limit: 1})
	storekit.Must(t, err)
	storekit.Same(t, "never-run first", []model.ReplicationTargetState{{Key: "k1", URL: replication("k1", "selected").URL}}, first)
	if next == "" {
		t.Fatal("missing next target page")
	}
	second, final, err := rs.StatusTargets(ctx, "selected", kernel.Page{Limit: 1, After: next})
	storekit.Must(t, err)
	storekit.Same(t, "never-run second", []model.ReplicationTargetState{{Key: "k2", URL: replication("k2", "selected").URL}}, second)
	if final != "" {
		t.Fatal("final target page has a next cursor")
	}
	completed := model.ReplicationRun{ReplicationKey: "k1", Started: storekit.At(1), Completed: storekit.At(2), Status: model.ReplicationOK}
	running := model.ReplicationRun{ReplicationKey: "k1", Started: storekit.At(3), Status: model.ReplicationIncomplete}
	for _, run := range []model.ReplicationRun{completed, running, {ReplicationKey: "k3", Started: storekit.At(4), Completed: storekit.At(5), Status: model.ReplicationError}} {
		storekit.Must(t, rs.PutRun(ctx, run))
	}
	states, _, err := rs.StatusTargets(ctx, "selected", kernel.Page{})
	storekit.Must(t, err)
	storekit.Same(t, "new incomplete retains prior completed time", model.ReplicationTargetState{
		Key: "k1", URL: replication("k1", "selected").URL, Latest: running, LastCompleted: completed.Completed,
	}, states[0])
	running.Completed, running.Status = storekit.At(6), model.ReplicationPartialFailure
	storekit.Must(t, rs.PutRun(ctx, running))
	states, _, err = rs.StatusTargets(ctx, "selected", kernel.Page{})
	storekit.Must(t, err)
	storekit.Same(t, "actual partial completion", model.ReplicationTargetState{
		Key: "k1", URL: replication("k1", "selected").URL, Latest: running, LastCompleted: running.Completed,
	}, states[0])
	storekit.Must(t, rs.Delete(ctx, "k1"))
	storekit.Must(t, rs.Put(ctx, replication("k1", "selected")))
	states, _, err = rs.StatusTargets(ctx, "selected", kernel.Page{})
	storekit.Must(t, err)
	storekit.Same(t, "recreated target never inherits old history", model.ReplicationTargetState{
		Key: "k1", URL: replication("k1", "selected").URL,
	}, states[0])
	for _, after := range []string{"!", kernel.Cursor("one", "two")} {
		if _, _, err := rs.StatusTargets(ctx, "selected", kernel.Page{After: after}); !errors.Is(err, kernel.ErrInvalidCursor) {
			t.Fatalf("invalid status cursor accepted: %v", err)
		}
	}
	empty, cursor, err := rs.StatusTargets(ctx, "missing", kernel.Page{})
	if err != nil || len(empty) != 0 || cursor != "" {
		t.Fatalf("missing repository projection %+v %q %v", empty, cursor, err)
	}
}
