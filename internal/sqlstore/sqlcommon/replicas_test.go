package sqlcommon_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// TestAccReplicaDriverFailures covers the replica index's driver error paths: a
// row that cannot be scanned and a table that is gone. Each must surface as an
// error that is neither ErrNotFound nor ErrConflict.
func TestAccReplicaDriverFailures(t *testing.T) {
	ctx := context.Background()
	s := openContent(t, filepath.Join(t.TempDir(), "aw.db"), nil)
	execSQL(
		t,
		s,
		"INSERT INTO blob_replicas (sha256, member, state, size) VALUES ('a', 'm', 'present', 'not a number')",
	)
	r := s.Replicas()
	check := func(what string, err error) {
		t.Helper()
		if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			t.Fatalf("%s: want a driver error, got %v", what, err)
		}
	}
	_, err := r.ForBlob(ctx, "a")
	check("scan ForBlob", err)
	_, _, err = r.List(ctx, store.ReplicaFilter{}, store.Page{})
	check("scan List", err)
	_, err = r.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts sums the size column and must still work: %v", err)
	}
	execSQL(t, s, "DROP TABLE blob_replicas")
	_, err = r.Counts(ctx)
	check("Counts", err)
	_, _, err = r.List(ctx, store.ReplicaFilter{Member: "m"}, store.Page{})
	check("List", err)
}
