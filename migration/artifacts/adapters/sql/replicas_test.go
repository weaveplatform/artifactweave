package sql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// TestAccReplicaDriverFailures covers the replica index's driver error paths: a
// row that cannot be scanned and a table that is gone. Each must surface as an
// error that is neither ErrNotFound nor ErrConflict.
func TestAccReplicaDriverFailures(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	sqltest.Exec(
		t,
		s.DB(),
		"INSERT INTO blob_replicas (sha256, member, state, size) VALUES ('a', 'm', 'present', 'not a number')",
	)
	r := s.Replicas()
	check := func(what string, err error) {
		t.Helper()
		if err == nil || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("%s: want a driver error, got %v", what, err)
		}
	}
	_, err := r.ForBlob(ctx, "a")
	check("scan ForBlob", err)
	_, _, err = r.List(ctx, mirror.ReplicaFilter{}, kernel.Page{})
	check("scan List", err)
	_, err = r.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts sums the size column and must still work: %v", err)
	}
	sqltest.Exec(t, s.DB(), "DROP TABLE blob_replicas")
	_, err = r.Counts(ctx)
	check("Counts", err)
	_, _, err = r.List(ctx, mirror.ReplicaFilter{Member: "m"}, kernel.Page{})
	check("List", err)
}
