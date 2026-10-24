package sqlcommon_test

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The contract suite covers the work queue's behaviour; these drive its
// driver-failure paths.

func TestAccWorkClosedDatabase(t *testing.T) {
	s := entOpen(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := s.WorkQueue()
	now := time.Now()
	req := store.WorkLease{Holder: "w", Limit: 1, Until: now.Add(time.Minute), Now: now}
	leaseKey := func() error {
		_, _, err := q.LeaseKey(ctx, "k", "x", req)
		return err
	}
	entFails(t, map[string]error{
		"Enqueue":  q.Enqueue(ctx, "k", "x", nil, now),
		"Lease":    entErr(q.Lease(ctx, req)),
		"LeaseKey": leaseKey(),
		"Extend":   q.Extend(ctx, "k", "x", 1, now.Add(time.Minute), now),
		"Complete": q.Complete(ctx, "k", "x", 1, now),
		"Fail":     q.Fail(ctx, "k", "x", 1, store.WorkFailure{Park: true, Now: now}),
		"Release":  q.Release(ctx, "k", "x", 1, now),
		"Get":      entErr(q.Get(ctx, "k", "x")),
		"Stats":    entErr(q.Stats(ctx)),
	})
}

// A lease whose update fails takes nothing.
func TestAccWorkLeaseUpdateFails(t *testing.T) {
	s := entOpen(t)
	ctx := context.Background()
	q := s.WorkQueue()
	now := time.Now()
	if err := q.Enqueue(ctx, "k", "x", nil, now); err != nil {
		t.Fatal(err)
	}
	entExec(t, s, "CREATE TRIGGER fail_work BEFORE UPDATE ON work_items"+
		" BEGIN SELECT RAISE(ABORT, 'injected'); END")
	req := store.WorkLease{Holder: "w", Limit: 1, Until: now.Add(time.Minute), Now: now}
	_, leaseErr := q.Lease(ctx, req)
	_, _, keyErr := q.LeaseKey(ctx, "k", "x", req)
	entFails(t, map[string]error{"Lease": leaseErr, "LeaseKey": keyErr})
	it, err := q.Get(ctx, "k", "x")
	if err != nil || it.State != types.WorkPending || it.Generation != 0 {
		t.Fatalf("after failed leases: %+v %v", it, err)
	}
}
