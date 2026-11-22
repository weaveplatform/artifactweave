package replication_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type dispatchStore struct {
	*fedinmem.Store
	platform *platforminmem.Store
}

func (s dispatchStore) WorkQueue() work.WorkQueue { return s.platform.WorkQueue() }

func coordinatorFixture(t *testing.T, key string, scheduled bool) (*world, dispatchStore, replication.Coordinator, context.Context) {
	t.Helper()
	w := newWorld()
	db := dispatchStore{Store: w.db, platform: platforminmem.New(w.db.Store)}
	ctx := accessapi.WithCaller(t.Context(), kernel.Principal{Name: "schedule-author", TokenID: "verified-test-token"}, nil)
	input := replication.Input{URL: ptr("https://target.example/artifactory/destination"), Enabled: ptr(true), SyncProperties: ptr(false)}
	if key == "remote" {
		input.URL = nil
	}
	if scheduled {
		input.CronExp = ptr("0 0 2 * * ?")
	}
	must(t, w.service.Replace(ctx, key, input))
	coordinator := replication.Coordinator{Store: db, Configurations: w.service, Now: func() time.Time { return time.Unix(1, 0) }}
	start := func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
		if !db.InTx(ctx) {
			t.Fatal("transfer/execution admission did not share a transaction")
		}
		if err := db.platform.Settings().Put(ctx, "admitted-transfer:"+request.ID, true); err != nil {
			return replication.TransferAdmission{}, err
		}
		return replication.TransferAdmission{
			ID: request.ID, Incarnation: "transfer-" + request.ID,
			Repository: artifactsapi.TransferRepository{Key: key, Incarnation: "repository-lifetime", Generation: "generation", PackageType: repo.Generic},
		}, nil
	}
	coordinator.Drivers = map[model.ReplicationDirection]replication.TransferStart{model.ReplicationPush: start, model.ReplicationPull: start}
	coordinator.Restore = func(ctx context.Context, reference kernel.CredentialReference) (context.Context, error) {
		if db.InTx(ctx) || reference.Name != "schedule-author" {
			t.Fatal("schedule author restoration borrowed SQL or another author")
		}
		return accessapi.WithCaller(ctx, reference.Principal(), nil), nil
	}
	return w, db, coordinator, ctx
}

func TestUnitReplicationManualAdmissionsKeepDistinctRunsForBothDirections(t *testing.T) {
	for _, key := range []string{"local", "remote"} {
		t.Run(key, func(t *testing.T) {
			_, db, coordinator, ctx := coordinatorFixture(t, key, false)
			first, err := coordinator.Execute(ctx, key, "selected")
			must(t, err)
			second, err := coordinator.Execute(ctx, key, "selected")
			must(t, err)
			if first.Reference == second.Reference || first.Binding.TransferID == second.Binding.TransferID || first.Status() != model.ReplicationIncomplete {
				t.Fatal("simultaneous manual requests coalesced or reported false completion")
			}
			items, err := db.WorkQueue().Lease(ctx, work.WorkLease{
				Kinds: []string{replication.ExecutionWorkKind}, Holder: "worker", Limit: 10,
				Now: time.Unix(2, 0), Until: time.Unix(62, 0),
			})
			must(t, err)
			if len(items) != 2 {
				t.Fatal("distinct durable executions did not have distinct queue work")
			}
			for _, item := range items {
				var ref model.ExecutionReference
				must(t, json.Unmarshal(item.Payload, &ref))
				if ref != first.Reference && ref != second.Reference {
					t.Fatal("execution payload was not an immutable reference")
				}
			}
		})
	}
}

func TestUnitReplicationScheduledReplayPreservesLeasedRunAndManualIndependence(t *testing.T) {
	w, db, coordinator, ctx := coordinatorFixture(t, "local", true)
	target := stored(t, w, "local")
	trigger := schedule.Trigger{
		Reference: schedule.Reference{Kind: replication.ScheduleKind, Key: target.Key},
		Revision:  target.Revision, Fire: time.Unix(1, 0), ID: "logical-fire", Fence: func(ctx context.Context) error {
			if !db.InTx(ctx) {
				t.Fatal("schedule fence escaped admission transaction")
			}
			return nil
		},
	}
	first, err := coordinator.Scheduled(t.Context(), trigger)
	must(t, err)
	items, err := db.WorkQueue().Lease(ctx, work.WorkLease{
		Kinds: []string{replication.ExecutionWorkKind}, Holder: "worker", Limit: 10,
		Now: time.Unix(2, 0), Until: time.Unix(62, 0),
	})
	must(t, err)
	if len(items) != 1 {
		t.Fatal("occurrence did not admit exactly one queued execution")
	}
	replayed, err := coordinator.Scheduled(t.Context(), trigger)
	must(t, err)
	current, err := db.WorkQueue().Get(ctx, items[0].Kind, items[0].Key)
	must(t, err)
	if replayed.Reference != first.Reference || !current.DirtySince.IsZero() || current.Generation != items[0].Generation {
		t.Fatal("occurrence replay restarted or replaced leased execution")
	}
	manual, err := coordinator.Execute(ctx, "local", "")
	must(t, err)
	if manual.Reference == first.Reference || manual.Binding.Occurrence != "" {
		t.Fatal("manual request borrowed the scheduled logical execution")
	}
}

func TestUnitReplicationAdmissionControlsAndFinalTriggerFenceRollBackAllState(t *testing.T) {
	w, db, coordinator, ctx := coordinatorFixture(t, "local", true)
	target := stored(t, w, "local")
	trigger := schedule.Trigger{
		Reference: schedule.Reference{Kind: replication.ScheduleKind, Key: target.Key}, Revision: target.Revision,
		Fire: time.Unix(1, 0), ID: "refused-fire", Fence: func(context.Context) error { return kernel.ErrConflict },
	}
	if _, err := coordinator.Scheduled(t.Context(), trigger); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("expired occurrence committed an execution", err)
	}
	if _, err := db.Executions().ForOccurrence(ctx, trigger.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("rejected trigger left durable dispatch evidence", err)
	}
	var transferred bool
	if err := db.platform.Settings().Get(ctx, "admitted-transfer:"+trigger.ID, &transferred); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("rejected trigger left direction progress", err)
	}
	must(t, w.service.SetBlocked(ctx, true, true, false))
	if _, err := coordinator.Execute(ctx, "local", ""); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("globally blocked push admitted execution", err)
	}
	stats, err := db.WorkQueue().Stats(ctx)
	must(t, err)
	if len(stats) != 0 {
		t.Fatal("refused admission left queued work")
	}
}
