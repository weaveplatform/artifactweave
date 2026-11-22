package replication_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitReplicationInvalidResolvedOptionsNeverAdmitDirectionWork(t *testing.T) {
	var nulScope replication.ExecutionInput
	must(t, json.Unmarshal([]byte(`{"include":"selected/\u0000**"}`), &nulScope))
	for _, tc := range []struct {
		name    string
		input   replication.ExecutionInput
		removed bool
		want    error
	}{
		{name: "unknown proxy", input: replication.ExecutionInput{Proxy: ptr("unknownProxy")}, want: kernel.ErrInvalid},
		{name: "JSON NUL selection", input: nulScope, want: kernel.ErrInvalid},
		{name: "private username text", input: replication.ExecutionInput{Username: ptr("private-lab-user\x00")}, want: kernel.ErrInvalid},
		{name: "private password text", input: replication.ExecutionInput{Password: ptr("private-lab-password\x00")}, want: kernel.ErrInvalid},
		{name: "removed saved target", removed: true, want: kernel.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
			saved := stored(t, w, "local")
			if tc.removed {
				must(t, w.service.Delete(ctx, "local", ""))
			}
			started := false
			start := coordinator.Drivers[model.ReplicationPush]
			coordinator.Drivers[model.ReplicationPush] = func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
				started = true
				return start(ctx, request)
			}
			var run model.ExecutionRun
			var err error
			if tc.removed {
				run, err = coordinator.Execute(ctx, "local", "selected")
			} else {
				run, err = coordinator.ExecuteWithOptions(ctx, "local", "selected", tc.input)
			}
			if !errors.Is(err, tc.want) || run.Reference.Validate() == nil || started || strings.Contains(err.Error(), "private-lab") {
				t.Fatal("refused resolved options admitted work or disclosed private text", err)
			}
			stats, err := db.WorkQueue().Stats(ctx)
			must(t, err)
			if len(stats) != 0 {
				t.Fatal("refused resolved options retained queue work")
			}
			keys, err := db.platform.Settings().Keys(ctx)
			must(t, err)
			for _, key := range keys {
				if strings.HasPrefix(key, "admitted-transfer:") {
					t.Fatal("refused resolved options retained direction admission")
				}
			}
			if tc.removed {
				rows, err := db.Replications().ForRepo(ctx, "local")
				must(t, err)
				if len(rows) != 0 {
					t.Fatal("ordinary execution recreated a removed saved target")
				}
				must(t, db.Replications().Put(ctx, saved))
			} else if !reflect.DeepEqual(saved, stored(t, w, "local")) {
				t.Fatal("refused override changed the saved configuration")
			}
			healthy, err := coordinator.Execute(ctx, "local", "selected")
			must(t, err)
			if !started || healthy.Reference.Validate() != nil {
				t.Fatal("refused override prevented healthy saved-target admission")
			}
			var admitted bool
			must(t, db.platform.Settings().Get(ctx, "admitted-transfer:"+healthy.Reference.ID, &admitted))
			if !admitted {
				t.Fatal("healthy admission did not retain the actual direction marker")
			}
		})
	}
}

func TestUnitReplicationExplicitTargetRechecksCurrentAuthorityAndRepository(t *testing.T) {
	for _, stage := range []string{
		"wrong key", "wrong revision", "inner guard", "repository read", "repository removed", "repository became remote", "push blocked", "settings read",
	} {
		t.Run(stage, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
			saved := stored(t, w, "local")
			run, err := coordinator.ExecuteWithOptions(ctx, "local", "selected", replication.ExecutionInput{URL: ptr("https://explicit.example/artifactory/destination")})
			must(t, err)
			options, err := db.Executions().Options(ctx, run.Reference)
			must(t, err)
			if !options.Independent {
				t.Fatal("fixture did not retain an actual explicit admission")
			}
			items, err := db.WorkQueue().Lease(ctx, work.WorkLease{
				Kinds: []string{replication.ExecutionWorkKind}, Holder: "explicit-options-test", Limit: 1,
				Now: time.Unix(2, 0), Until: time.Unix(62, 0),
			})
			must(t, err)
			if len(items) != 1 {
				t.Fatal("explicit admission did not retain its queue reference")
			}
			position := items[0]
			repository := w.repos.values["local"]
			service := w.service.WithExecutionOptions(&options)
			key, revision := run.Binding.TargetKey, run.Binding.TargetRevision
			var want error = kernel.ErrConflict
			guarded := false
			switch stage {
			case "wrong key":
				key = "another-explicit-target"
			case "wrong revision":
				revision = "another-explicit-revision"
			case "inner guard":
				service.Guard = func(ctx context.Context) error {
					guarded = true
					if !db.InTx(ctx) {
						t.Fatal("explicit worker guard escaped its transaction")
					}
					return kernel.ErrForbidden
				}
				want = kernel.ErrForbidden
			case "repository read":
				w.repos.metadataError, want = errInjected, errInjected
			case "repository removed":
				delete(w.repos.values, "local")
				want = kernel.ErrNotFound
			case "repository became remote":
				changed := repository
				changed.Class = repo.Remote
				w.repos.values["local"] = changed
			case "push blocked":
				must(t, w.service.SetBlocked(ctx, true, true, false))
			case "settings read":
				service.Settings = failedSettings{SettingsStore: w.service.Settings, read: true}
				want = errInjected
			}
			entered := false
			callback := func(ctx context.Context, target model.Replication) error {
				entered = true
				if !db.InTx(ctx) ||
					target != options.Apply(model.Replication{RepoKey: options.RepositoryKey, Key: options.TargetKey, Revision: options.TargetRevision, Enabled: true}) {
					t.Fatal("explicit worker callback lost its transaction or immutable options")
				}
				return db.platform.Settings().Put(ctx, "explicit-callback", true)
			}
			err = service.WithTarget(ctx, key, revision, callback)
			if !errors.Is(err, want) || entered || stage == "inner guard" && !guarded {
				t.Fatal("explicit admission retained stale identity or bypassed the intended refusal", err)
			}
			var marker bool
			if err := db.platform.Settings().Get(ctx, "explicit-callback", &marker); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("refused explicit worker callback published a marker", err)
			}
			current, err := db.Executions().Run(ctx, run.Reference)
			must(t, err)
			private, err := db.Executions().Options(ctx, run.Reference)
			must(t, err)
			queued, err := db.WorkQueue().Get(ctx, position.Kind, position.Key)
			must(t, err)
			if !reflect.DeepEqual(run, current) || private != options || !reflect.DeepEqual(position, queued) || !reflect.DeepEqual(saved, stored(t, w, "local")) {
				t.Fatal("explicit refusal mutated retained run, private options, queue or saved configuration")
			}
			w.repos.metadataError = nil
			w.repos.values["local"] = repository
			if stage == "push blocked" {
				must(t, w.service.SetBlocked(ctx, false, true, false))
			}
			must(t, w.service.WithExecutionOptions(&options).WithTarget(ctx, options.TargetKey, options.TargetRevision, callback))
			if !entered {
				t.Fatal("healthy explicit worker admission did not recover")
			}
			must(t, db.platform.Settings().Get(ctx, "explicit-callback", &marker))
			if !marker {
				t.Fatal("healthy explicit worker callback did not commit")
			}
		})
	}
}
