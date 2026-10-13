package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runReplicas covers the replica index of both tiers (decision 0028).
func runReplicas(t *testing.T, newStore Factory) {
	t.Run("Replicas", func(t *testing.T) { testReplicas(t, newStore(t).Replicas()) })
	t.Run(
		"ColdReplicas",
		func(t *testing.T) { testReplicas(t, newStore(t).Archive().ColdReplicas()) },
	)
	t.Run("ReplicaTiersSeparate", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		must(t, s.Replicas().Put(ctx, replica(1, "aws", types.ReplicaPresent, 1)))
		_, err := s.Archive().ColdReplicas().Get(ctx, replica(1, "aws", "", 0).SHA256, "aws")
		wantErr(t, err, store.ErrNotFound)
	})
}

// replica builds the record of fixture blob i on member, updated at minute m.
func replica(i int, member string, state types.ReplicaState, m int) types.BlobReplica {
	return types.BlobReplica{
		SHA256: fmt.Sprintf("%064x", i), Member: member, State: state,
		Size: int64(10 * i), Updated: at(m),
	}
}

func testReplicas(t *testing.T, r store.ReplicaIndex) {
	ctx := context.Background()
	wantErr(t, r.Put(ctx, types.BlobReplica{Member: "aws"}), store.ErrInvalid)
	wantErr(t, r.Put(ctx, types.BlobReplica{SHA256: "x"}), store.ErrInvalid)

	fixtures := []types.BlobReplica{
		replica(1, "aws", types.ReplicaPresent, 5),
		replica(1, "gcp", types.ReplicaPending, 3),
		replica(1, "azure", types.ReplicaPending, 4),
		replica(2, "aws", types.ReplicaPresent, 1),
		replica(2, "gcp", types.ReplicaFailed, 2),
		replica(3, "gcp", types.ReplicaPending, 0),
	}
	fixtures[5].Updated = fixtures[5].Updated.AddDate(-1, 0, 0) // oldest
	failed := fixtures[4]
	failed.Attempts, failed.LastError = 5, "bucket unreachable"
	fixtures[4] = failed
	for _, f := range fixtures {
		must(t, r.Put(ctx, f))
	}
	same(t, "Get", failed, get[types.BlobReplica](t)(r.Get(ctx, failed.SHA256, "gcp")))
	_, err := r.Get(ctx, failed.SHA256, "nowhere")
	wantErr(t, err, store.ErrNotFound)

	// Put replaces; an unset time comes back unset and sorts first.
	repaired := fixtures[1]
	repaired.State, repaired.Updated = types.ReplicaPresent, at(9)
	must(t, r.Put(ctx, repaired))
	noTime := types.BlobReplica{
		SHA256: fmt.Sprintf("%064x", 4),
		Member: "aws",
		State:  types.ReplicaPending,
	}
	must(t, r.Put(ctx, noTime))
	if got := get[types.BlobReplica](t)(r.Get(ctx, noTime.SHA256, "aws")); !got.Updated.IsZero() {
		t.Fatalf("unset Updated came back as %v", got.Updated)
	}

	forBlob := get[[]types.BlobReplica](t)(r.ForBlob(ctx, fixtures[0].SHA256))
	var members []string
	for _, v := range forBlob {
		members = append(members, v.Member)
	}
	same(t, "ForBlob members", []string{"aws", "azure", "gcp"}, members)

	// The repair queue: pending, oldest first, across pages.
	pending := pages(t, 1, func(p store.Page) ([]types.BlobReplica, string, error) {
		return r.List(
			ctx,
			store.ReplicaFilter{States: []types.ReplicaState{types.ReplicaPending}},
			p,
		)
	})
	var order []string
	for _, v := range pending {
		order = append(order, fmt.Sprintf("%s@%s", v.SHA256[63:], v.Member))
	}
	same(t, "pending queue", []string{"4@aws", "3@gcp", "1@azure"}, order)

	gcp := get[[]types.BlobReplica](
		t,
	)(
		firstPage(r.List(ctx, store.ReplicaFilter{Member: "gcp"}, store.Page{})),
	)
	if len(gcp) != 3 {
		t.Fatalf("member filter: %d records", len(gcp))
	}
	all := pages(t, 2, func(p store.Page) ([]types.BlobReplica, string, error) {
		return r.List(ctx, store.ReplicaFilter{}, p)
	})
	if len(all) != 7 {
		t.Fatalf("unfiltered list: %d records", len(all))
	}
	_, _, err = r.List(ctx, store.ReplicaFilter{}, store.Page{After: store.Cursor("x", "y", "z")})
	wantErr(t, err, store.ErrInvalidCursor)

	counts := get[[]store.ReplicaCount](t)(r.Counts(ctx))
	same(t, "Counts", []store.ReplicaCount{
		{Member: "aws", State: types.ReplicaPending, Count: 1, Bytes: 0},
		{Member: "aws", State: types.ReplicaPresent, Count: 2, Bytes: 30},
		{Member: "azure", State: types.ReplicaPending, Count: 1, Bytes: 10},
		{Member: "gcp", State: types.ReplicaFailed, Count: 1, Bytes: 20},
		{Member: "gcp", State: types.ReplicaPending, Count: 1, Bytes: 30},
		{Member: "gcp", State: types.ReplicaPresent, Count: 1, Bytes: 10},
	}, counts)

	must(t, r.Delete(ctx, failed.SHA256, "gcp"))
	wantErr(t, r.Delete(ctx, failed.SHA256, "gcp"), store.ErrNotFound)
	n, err := r.DeleteBlob(ctx, fixtures[0].SHA256)
	if err != nil || n != 3 {
		t.Fatalf("DeleteBlob = %d, %v", n, err)
	}
	if left := get[[]types.BlobReplica](t)(r.ForBlob(ctx, fixtures[0].SHA256)); len(left) != 0 {
		t.Fatalf("records left after DeleteBlob: %v", left)
	}
}

// firstPage drops the cursor of a list result.
func firstPage[V any](rows []V, _ string, err error) ([]V, error) { return rows, err }
