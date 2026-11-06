package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runReplications covers replication configurations and their last runs.
func runReplications(t *testing.T, newStore Factory) {
	t.Run("Replications", func(t *testing.T) { testReplications(t, newStore(t)) })
}

func replication(key, repoKey string) types.Replication {
	return types.Replication{
		RepoKey:              repoKey,
		Key:                  key,
		URL:                  "https://edge.example/artifactory/" + repoKey,
		Username:             "replicator",
		Password:             "pw-" + key,
		Enabled:              true,
		CronExp:              "0 0 * * * ?",
		EventReplication:     true,
		SyncDeletes:          true,
		SyncProperties:       true,
		IncludePathPrefix:    "org/",
		ExcludePathPrefix:    "org/tmp/",
		Proxy:                "corp",
		CheckBinaryExistence: true,
		SocketTimeout:        15*time.Second + 1234*time.Microsecond,
	}
}

func testReplications(t *testing.T, s store.Store) {
	ctx := context.Background()
	rs := s.Replications()
	wantErr(t, rs.Put(ctx, replication("", "repo-a")), store.ErrInvalid)
	k1, k2, k3 := replication(
		"k1",
		"repo-a",
	), replication(
		"k2",
		"repo-a",
	), replication(
		"k3",
		"repo-b",
	)
	for _, r := range []types.Replication{k3, k2, k1} {
		must(t, rs.Put(ctx, r))
	}
	got := get[types.Replication](t)(rs.Get(ctx, "k1"))
	same(t, "Get", k1, got)
	if got.SocketTimeout != 15001*time.Millisecond {
		t.Fatalf("socket timeout = %v, want whole milliseconds", got.SocketTimeout)
	}
	_, err := rs.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
	same(
		t,
		"ForRepo",
		[]types.Replication{k1, k2},
		get[[]types.Replication](t)(rs.ForRepo(ctx, "repo-a")),
	)
	same(t, "List", []types.Replication{k1, k2, k3}, get[[]types.Replication](t)(rs.List(ctx)))

	k1.Password, k1.Enabled = "rotated", false
	must(t, rs.Put(ctx, k1))
	same(t, "replaced", k1, get[types.Replication](t)(rs.Get(ctx, "k1")))
	k2.Password = ""
	must(t, rs.Put(ctx, k2))
	same(t, "no password", k2, get[types.Replication](t)(rs.Get(ctx, "k2")))

	wantErr(
		t,
		rs.PutRun(ctx, types.ReplicationRun{ReplicationKey: "nope", Started: at(1)}),
		store.ErrNotFound,
	)
	running := types.ReplicationRun{
		ReplicationKey: "k1",
		Started:        at(1),
		Status:         types.ReplicationIncomplete,
	}
	must(t, rs.PutRun(ctx, running))
	same(t, "LastRun running", running, get[types.ReplicationRun](t)(rs.LastRun(ctx, "k1")))
	done := types.ReplicationRun{
		ReplicationKey: "k1",
		Started:        at(1),
		Completed:      at(2),
		Status:         types.ReplicationOK,
	}
	must(t, rs.PutRun(ctx, done))
	same(t, "LastRun replaced", done, get[types.ReplicationRun](t)(rs.LastRun(ctx, "k1")))
	later := types.ReplicationRun{
		ReplicationKey: "k1",
		Started:        at(5),
		Status:         types.ReplicationError,
	}
	must(t, rs.PutRun(ctx, later))
	must(
		t,
		rs.PutRun(
			ctx,
			types.ReplicationRun{ReplicationKey: "k2", Started: at(3), Status: types.ReplicationOK},
		),
	)
	same(t, "LastRun newest", later, get[types.ReplicationRun](t)(rs.LastRun(ctx, "k1")))
	_, err = rs.LastRun(ctx, "k3")
	wantErr(t, err, store.ErrNotFound)

	must(t, rs.Delete(ctx, "k1"))
	wantErr(t, rs.Delete(ctx, "k1"), store.ErrNotFound)
	_, err = rs.LastRun(ctx, "k1")
	wantErr(t, err, store.ErrNotFound)
	must(t, rs.DeleteForRepo(ctx, "repo-a"))
	must(t, rs.DeleteForRepo(ctx, "repo-none"))
	_, err = rs.LastRun(ctx, "k2")
	wantErr(t, err, store.ErrNotFound)
	same(t, "after deletes", []types.Replication{k3}, get[[]types.Replication](t)(rs.List(ctx)))
}
