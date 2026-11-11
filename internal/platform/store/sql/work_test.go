package sql_test

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// The contract suite covers the work queue's behaviour; these drive its
// driver-failure paths.

func TestAccWorkClosedDatabase(t *testing.T) {
	s := platsql.New(sqltest.OpenSQLite(t, platsql.Module))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := s.WorkQueue()
	now := time.Now()
	req := work.WorkLease{Holder: "w", Limit: 1, Until: now.Add(time.Minute), Now: now}
	leaseKey := func() error {
		_, _, err := q.LeaseKey(ctx, "k", "x", req)
		return err
	}
	sqltest.Fails(t, map[string]error{
		"Enqueue":  q.Enqueue(ctx, "k", "x", nil, now),
		"Lease":    sqltest.Err(q.Lease(ctx, req)),
		"LeaseKey": leaseKey(),
		"Extend":   q.Extend(ctx, "k", "x", 1, now.Add(time.Minute), now),
		"Complete": q.Complete(ctx, "k", "x", 1, now),
		"Fail":     q.Fail(ctx, "k", "x", 1, work.WorkFailure{Park: true, Now: now}),
		"Release":  q.Release(ctx, "k", "x", 1, now),
		"Get":      sqltest.Err(q.Get(ctx, "k", "x")),
		"Stats":    sqltest.Err(q.Stats(ctx)),
	})
}

// A lease whose update fails takes nothing.
func TestAccWorkLeaseUpdateFails(t *testing.T) {
	s := platsql.New(sqltest.OpenSQLite(t, platsql.Module))
	ctx := context.Background()
	q := s.WorkQueue()
	now := time.Now()
	if err := q.Enqueue(ctx, "k", "x", nil, now); err != nil {
		t.Fatal(err)
	}
	sqltest.Exec(t, s.DB(), sqltest.FailTrigger("UPDATE", "work_items"))
	req := work.WorkLease{Holder: "w", Limit: 1, Until: now.Add(time.Minute), Now: now}
	_, leaseErr := q.Lease(ctx, req)
	_, _, keyErr := q.LeaseKey(ctx, "k", "x", req)
	sqltest.Fails(t, map[string]error{"Lease": leaseErr, "LeaseKey": keyErr})
	it, err := q.Get(ctx, "k", "x")
	if err != nil || it.State != work.WorkPending || it.Generation != 0 {
		t.Fatalf("after failed leases: %+v %v", it, err)
	}
}
