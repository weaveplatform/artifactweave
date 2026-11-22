package replication_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
)

type occurrenceFaultStore struct {
	replication.CoordinatorStore
	targets port.ReplicationStore
}

func (s occurrenceFaultStore) Replications() port.ReplicationStore { return s.targets }

type occurrenceFaultTargets struct{ port.ReplicationStore }

func (p occurrenceFaultTargets) Get(context.Context, string) (model.Replication, error) {
	return model.Replication{}, errExecutionStorage
}

func TestUnitReplicationScheduledAdmissionRechecksOccurrenceAndRestoredContext(t *testing.T) {
	for _, stage := range []string{"guard", "target read", "disabled", "revision", "schedule removed", "author missing", "restore denied", "restore nil", "restore transaction", "changed during restore"} {
		t.Run(stage, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, "local", true)
			target := stored(t, w, "local")
			trigger := schedule.Trigger{
				Reference: schedule.Reference{Kind: replication.ScheduleKind, Key: target.Key},
				Revision:  target.Revision,
				Fire:      time.Unix(1, 0),
				ID:        "refused-occurrence",
				Fence:     func(context.Context) error { return nil },
			}
			var want error = kernel.ErrConflict
			originalRestore := coordinator.Restore
			restored := false
			coordinator.Restore = func(ctx context.Context, ref kernel.CredentialReference) (context.Context, error) {
				restored = true
				if db.InTx(ctx) || ref != target.ScheduleAuthor {
					t.Fatal("occurrence restored authority under a transaction or different author")
				}
				switch stage {
				case "restore denied":
					return nil, kernel.ErrBadCredentials
				case "restore nil":
					return nil, nil
				case "restore transaction":
					var leaked context.Context
					must(t, db.Tx(ctx, func(ctx context.Context) error { leaked = ctx; return nil }))
					return leaked, nil
				case "changed during restore":
					changed := target
					changed.Revision = "replacement-revision"
					must(t, db.Replications().Put(ctx, changed))
				}
				return originalRestore(ctx, ref)
			}
			switch stage {
			case "guard":
				coordinator.Configurations.Guard = func(context.Context) error { return kernel.ErrForbidden }
				want = kernel.ErrForbidden
			case "target read":
				coordinator.Store = occurrenceFaultStore{CoordinatorStore: db, targets: occurrenceFaultTargets{db.Replications()}}
				want = errExecutionStorage
			case "disabled":
				target.Enabled = false
			case "revision":
				target.Revision = "replacement-revision"
			case "schedule removed":
				target.CronExp = ""
			case "author missing":
				target.ScheduleAuthor = kernel.CredentialReference{}
			case "restore denied":
				want = kernel.ErrBadCredentials
			case "restore nil", "restore transaction":
				want = kernel.ErrStore
			}
			must(t, db.Replications().Put(ctx, target))
			got, err := coordinator.Scheduled(t.Context(), trigger)
			if !errors.Is(err, want) || got.Reference.Validate() == nil {
				t.Fatal("refused occurrence returned an admitted execution", err)
			}
			if stage == "guard" || stage == "target read" || stage == "disabled" || stage == "revision" || stage == "schedule removed" || stage == "author missing" {
				if restored {
					t.Fatal("invalid occurrence restored private author credentials")
				}
			} else if !restored {
				t.Fatal("restoration refusal did not reach the required seam")
			}
			if _, err := db.Executions().ForOccurrence(ctx, trigger.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("refused occurrence retained logical progress", err)
			}
			var admitted bool
			if err := db.platform.Settings().Get(ctx, "admitted-transfer:"+trigger.ID, &admitted); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("refused occurrence retained direction progress", err)
			}
			stats, err := db.WorkQueue().Stats(ctx)
			must(t, err)
			if len(stats) != 0 {
				t.Fatal("refused occurrence retained queue work")
			}
		})
	}
}

func TestUnitReplicationAdmissionRejectsUnavailableOrMismatchedDirectionProof(t *testing.T) {
	for _, stage := range []string{"repository read", "unsupported class", "missing driver", "missing credential", "transfer error", "foreign repository"} {
		t.Run(stage, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
			saved := stored(t, w, "local")
			started := false
			start := coordinator.Drivers[model.ReplicationPush]
			coordinator.Drivers[model.ReplicationPush] = func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
				started = true
				admission, err := start(ctx, request)
				if stage == "transfer error" {
					return admission, errExecutionStorage
				}
				if stage == "foreign repository" {
					admission.Repository.Key = "another-repository"
				}
				return admission, err
			}
			var want error = kernel.ErrInvalid
			switch stage {
			case "repository read":
				w.repos.metadataError = errExecutionStorage
				want = errExecutionStorage
			case "unsupported class":
				current := w.repos.values["local"]
				current.Class = repo.Virtual
				w.repos.values["local"] = current
			case "missing driver":
				delete(coordinator.Drivers, model.ReplicationPush)
			case "missing credential":
				ctx = t.Context()
				want = kernel.ErrUnauthenticated
			case "transfer error", "foreign repository":
				want = kernel.ErrStore
			}
			got, err := coordinator.Execute(ctx, "local", "selected")
			if !errors.Is(err, want) || got.Reference.Validate() == nil {
				t.Fatal("failed direction admission returned usable execution", err)
			}
			if started != (stage == "transfer error" || stage == "foreign repository") {
				t.Fatal("direction started before current repository/credential checks")
			}
			if !reflect.DeepEqual(saved, stored(t, w, "local")) {
				t.Fatal("refused execution changed saved configuration")
			}
			stats, err := db.WorkQueue().Stats(ctx)
			must(t, err)
			if len(stats) != 0 {
				t.Fatal("failed direction admission retained queue work")
			}
		})
	}
}

func TestUnitReplicationAdmissionFailureRollsBackDriverWrite(t *testing.T) {
	_, db, coordinator, ctx := coordinatorFixture(t, "local", false)
	var id string
	coordinator.Drivers[model.ReplicationPush] = func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
		id = request.ID
		must(t, db.platform.Settings().Put(ctx, "failed-driver:"+id, true))
		return replication.TransferAdmission{Repository: artifactsapi.TransferRepository{Key: "another-repository"}}, nil
	}
	_, err := coordinator.Execute(ctx, "local", "selected")
	if !errors.Is(err, kernel.ErrStore) || id == "" {
		t.Fatal("foreign direction proof was accepted or driver was not reached", err)
	}
	var marker bool
	if err := db.platform.Settings().Get(ctx, "failed-driver:"+id, &marker); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("invalid transfer proof retained the driver's transaction write", err)
	}
}

func TestUnitReplicationConfiguredEndpointMustNameARepositoryBeforeAdmission(t *testing.T) {
	w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
	target := stored(t, w, "local")
	target.URL = "https://legacy.example"
	must(t, db.Replications().Put(ctx, target))
	var attempted string
	start := coordinator.Drivers[model.ReplicationPush]
	coordinator.Drivers[model.ReplicationPush] = func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
		attempted = request.ID
		return start(ctx, request)
	}
	run, err := coordinator.Execute(ctx, "local", "selected")
	if !errors.Is(err, kernel.ErrInvalid) || run.Reference.Validate() == nil || attempted == "" {
		t.Fatal("invalid configured endpoint admitted durable dispatch or missed the driver's transaction write", err)
	}
	var admitted bool
	if err := db.platform.Settings().Get(ctx, "admitted-transfer:"+attempted, &admitted); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("invalid endpoint retained direction admission", err)
	}
	if !reflect.DeepEqual(target, stored(t, w, "local")) {
		t.Fatal("execution rewrote legacy endpoint configuration")
	}
}
