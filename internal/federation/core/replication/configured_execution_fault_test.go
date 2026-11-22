package replication_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitReplicationConfiguredWorkerRechecksTargetCatalogueAndCurrentControls(t *testing.T) {
	for _, stage := range []string{"target catalogue read", "repository read", "repository became virtual", "legacy event owner", "legacy checksum owner", "settings read"} {
		t.Run(stage, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
			saved := stored(t, w, "local")
			run, err := coordinator.ExecuteWithOptions(ctx, "local", "selected", replication.ExecutionInput{SyncProperties: ptr(true)})
			must(t, err)
			options, err := db.Executions().Options(ctx, run.Reference)
			must(t, err)
			if options.Independent || run.Binding.TargetKey != saved.Key || run.Binding.TargetRevision != saved.Revision {
				t.Fatal("fixture did not admit a real saved-target execution")
			}
			items, err := db.WorkQueue().Lease(ctx, work.WorkLease{
				Kinds: []string{replication.ExecutionWorkKind}, Holder: "configured-options-test", Limit: 1,
				Now: time.Unix(2, 0), Until: time.Unix(62, 0),
			})
			must(t, err)
			if len(items) != 1 {
				t.Fatal("configured admission did not retain its queue reference")
			}
			position := items[0]
			repository := w.repos.values["local"]
			service := w.service.WithExecutionOptions(&options)
			currentTarget := saved
			var want error = errInjected
			switch stage {
			case "target catalogue read":
				service.Store = failedRefreshReadStore{w.db}
			case "repository read":
				w.repos.metadataError = errInjected
			case "repository became virtual":
				changed := repository
				changed.Class = repo.Virtual
				w.repos.values["local"] = changed
				want = kernel.ErrConflict
			case "legacy event owner", "legacy checksum owner":
				currentTarget.EventReplication = stage == "legacy event owner"
				currentTarget.CheckBinaryExistence = stage == "legacy checksum owner"
				currentTarget.Revision = "legacy-owner-revision"
				must(t, db.Replications().Put(ctx, currentTarget))
				want = kernel.ErrConflict
			case "settings read":
				service.Settings = failedSettings{SettingsStore: w.service.Settings, read: true}
			}
			entered := false
			callbackTarget := saved
			callback := func(ctx context.Context, target model.Replication) error {
				entered = true
				if !db.InTx(ctx) || !reflect.DeepEqual(target, options.Apply(callbackTarget)) {
					t.Fatal("configured callback lost its transaction or immutable execution options")
				}
				return db.platform.Settings().Put(ctx, "configured-callback", true)
			}
			// A legacy owner transition rotates its revision; even a fresh caller
			// holding that current revision cannot execute unsupported capabilities.
			err = service.WithTarget(ctx, currentTarget.Key, currentTarget.Revision, callback)
			if !errors.Is(err, want) || entered {
				t.Fatal("configured worker bypassed current catalogue, repository or controls", err)
			}
			var marker bool
			if err := db.platform.Settings().Get(ctx, "configured-callback", &marker); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("refused configured worker callback published a marker", err)
			}
			assertRetained := func(target model.Replication) {
				t.Helper()
				current, err := db.Executions().Run(ctx, run.Reference)
				must(t, err)
				private, err := db.Executions().Options(ctx, run.Reference)
				must(t, err)
				queued, err := db.WorkQueue().Get(ctx, position.Kind, position.Key)
				must(t, err)
				if !reflect.DeepEqual(run, current) || private != options || !reflect.DeepEqual(position, queued) || !reflect.DeepEqual(target, stored(t, w, "local")) {
					t.Fatal("configured admission changed retained target, run, options or queue")
				}
			}
			assertRetained(currentTarget)
			w.repos.metadataError = nil
			w.repos.values["local"] = repository
			healthyTarget := saved
			if currentTarget.Revision != saved.Revision {
				must(t, w.service.Update(ctx, "local", replication.Input{EventReplication: ptr(false), CheckBinaryExistence: ptr(false)}))
				healthyTarget = stored(t, w, "local")
			}
			callbackTarget = healthyTarget
			must(t, w.service.WithExecutionOptions(&options).WithTarget(ctx, healthyTarget.Key, healthyTarget.Revision, callback))
			if !entered {
				t.Fatal("healthy configured callback did not recover")
			}
			must(t, db.platform.Settings().Get(ctx, "configured-callback", &marker))
			if !marker {
				t.Fatal("healthy configured callback did not commit")
			}
			assertRetained(healthyTarget)
		})
	}
}
