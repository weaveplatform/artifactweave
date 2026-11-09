package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/federation/core/model"
	"github.com/weaveplatform/artifactweave/migration/federation/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

// runReplications covers replication configurations and their last runs.
func runReplications(t *testing.T, newStore Factory) {
	t.Run("Replications", func(t *testing.T) { testReplications(t, newStore(t)) })
}

func replication(key, repoKey string) model.Replication {
	return model.Replication{
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

func testReplications(t *testing.T, s port.Store) {
	ctx := context.Background()
	rs := s.Replications()
	storekit.WantErr(t, rs.Put(ctx, replication("", "repo-a")), kernel.ErrInvalid)
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
	for _, r := range []model.Replication{k3, k2, k1} {
		storekit.Must(t, rs.Put(ctx, r))
	}
	got := storekit.Get[model.Replication](t)(rs.Get(ctx, "k1"))
	storekit.Same(t, "Get", k1, got)
	if got.SocketTimeout != 15001*time.Millisecond {
		t.Fatalf("socket timeout = %v, want whole milliseconds", got.SocketTimeout)
	}
	_, err := rs.Get(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(
		t,
		"ForRepo",
		[]model.Replication{k1, k2},
		storekit.Get[[]model.Replication](t)(rs.ForRepo(ctx, "repo-a")),
	)
	storekit.Same(
		t,
		"List",
		[]model.Replication{k1, k2, k3},
		storekit.Get[[]model.Replication](t)(rs.List(ctx)),
	)

	k1.Password, k1.Enabled = "rotated", false
	storekit.Must(t, rs.Put(ctx, k1))
	storekit.Same(t, "replaced", k1, storekit.Get[model.Replication](t)(rs.Get(ctx, "k1")))
	k2.Password = ""
	storekit.Must(t, rs.Put(ctx, k2))
	storekit.Same(t, "no password", k2, storekit.Get[model.Replication](t)(rs.Get(ctx, "k2")))

	storekit.WantErr(
		t,
		rs.PutRun(ctx, model.ReplicationRun{ReplicationKey: "nope", Started: storekit.At(1)}),
		kernel.ErrNotFound,
	)
	running := model.ReplicationRun{
		ReplicationKey: "k1",
		Started:        storekit.At(1),
		Status:         model.ReplicationIncomplete,
	}
	storekit.Must(t, rs.PutRun(ctx, running))
	storekit.Same(
		t,
		"LastRun running",
		running,
		storekit.Get[model.ReplicationRun](t)(rs.LastRun(ctx, "k1")),
	)
	done := model.ReplicationRun{
		ReplicationKey: "k1",
		Started:        storekit.At(1),
		Completed:      storekit.At(2),
		Status:         model.ReplicationOK,
	}
	storekit.Must(t, rs.PutRun(ctx, done))
	storekit.Same(
		t,
		"LastRun replaced",
		done,
		storekit.Get[model.ReplicationRun](t)(rs.LastRun(ctx, "k1")),
	)
	later := model.ReplicationRun{
		ReplicationKey: "k1",
		Started:        storekit.At(5),
		Status:         model.ReplicationError,
	}
	storekit.Must(t, rs.PutRun(ctx, later))
	storekit.Must(
		t,
		rs.PutRun(
			ctx,
			model.ReplicationRun{
				ReplicationKey: "k2",
				Started:        storekit.At(3),
				Status:         model.ReplicationOK,
			},
		),
	)
	storekit.Same(
		t,
		"LastRun newest",
		later,
		storekit.Get[model.ReplicationRun](t)(rs.LastRun(ctx, "k1")),
	)
	_, err = rs.LastRun(ctx, "k3")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	storekit.Must(t, rs.Delete(ctx, "k1"))
	storekit.WantErr(t, rs.Delete(ctx, "k1"), kernel.ErrNotFound)
	_, err = rs.LastRun(ctx, "k1")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Must(t, rs.DeleteForRepo(ctx, "repo-a"))
	storekit.Must(t, rs.DeleteForRepo(ctx, "repo-none"))
	_, err = rs.LastRun(ctx, "k2")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(
		t,
		"after deletes",
		[]model.Replication{k3},
		storekit.Get[[]model.Replication](t)(rs.List(ctx)),
	)
}
