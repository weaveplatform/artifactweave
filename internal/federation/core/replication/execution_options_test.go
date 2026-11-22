package replication_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitReplicationOneShotAdmissionRetainsResolvedOptionsWithoutChangingConfiguration(t *testing.T) {
	for _, key := range []string{"local", "remote"} {
		t.Run(key, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, key, false)
			saved := stored(t, w, key)
			direction, _ := model.DirectionFor(w.repos.values[key].Class)
			start := coordinator.Drivers[direction]
			var admitted *model.ExecutionOptions
			coordinator.Drivers[direction] = func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
				admitted = request.Options
				return start(ctx, request)
			}
			run, err := coordinator.ExecuteWithOptions(ctx, key, "selected", replication.ExecutionInput{
				Include: ptr("**/*.txt"), Exclude: ptr("**/excluded.txt"), SyncDeletes: ptr(true), SyncProperties: ptr(false),
			})
			must(t, err)
			if !run.Binding.OneShot || admitted == nil || !admitted.SyncDeletes || admitted.SyncProperties || admitted.Include != "**/*.txt" ||
				admitted.Exclude != "**/excluded.txt" {
				t.Fatal("direction driver did not receive the requested immutable values")
			}
			retained, err := db.Executions().Options(ctx, run.Reference)
			must(t, err)
			if retained != *admitted || retained.URL != saved.URL || retained.Username != saved.Username || retained.Password != saved.Password {
				t.Fatal("retained one-shot values differ from admitted connection/selection")
			}
			if !reflect.DeepEqual(saved, stored(t, w, key)) {
				t.Fatal("one-shot execution mutated the configured target")
			}
			defaults, err := coordinator.ExecuteWithOptions(ctx, key, "selected", replication.ExecutionInput{})
			must(t, err)
			values, err := db.Executions().Options(ctx, defaults.Reference)
			must(t, err)
			if !values.SyncProperties || values.SyncDeletes || values.Include != "" || values.Exclude != "" {
				t.Fatal("one-shot defaults were replaced by saved selection options")
			}
		})
	}
}

func TestUnitReplicationOneShotChangedAccountCannotInheritPassword(t *testing.T) {
	w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
	must(t, w.service.Update(ctx, "local", replication.Input{Username: ptr("saved-account"), Password: ptr("lab-saved-password")}))
	run, err := coordinator.ExecuteWithOptions(ctx, "local", "", replication.ExecutionInput{Username: ptr("different-account")})
	must(t, err)
	options, err := db.Executions().Options(ctx, run.Reference)
	must(t, err)
	if options.Username != "different-account" || options.Password != "" {
		t.Fatal("changed execution account inherited another account's password")
	}
}

func TestUnitReplicationOneShotInvalidScopeAndUnsupportedCapabilityDoNotEnqueue(t *testing.T) {
	_, db, coordinator, ctx := coordinatorFixture(t, "local", false)
	for _, input := range []replication.ExecutionInput{
		{URL: ptr("https://embedded:secret@different.example/artifactory/target")},
		{CheckBinaryExistence: ptr(true)},
	} {
		if _, err := coordinator.ExecuteWithOptions(ctx, "local", "", input); err == nil {
			t.Fatal("unsupported one-shot capability was admitted")
		}
	}
	if _, err := coordinator.ExecuteWithOptions(ctx, "local", "../outside", replication.ExecutionInput{}); err == nil {
		t.Fatal("invalid one-shot scope was admitted")
	}
	items, err := db.WorkQueue().Lease(ctx, work.WorkLease{
		Kinds: []string{replication.ExecutionWorkKind}, Holder: "fixture",
		Limit: 10, Now: time.Unix(2, 0), Until: time.Unix(62, 0),
	})
	must(t, err)
	if len(items) != 0 {
		t.Fatal("refused one-shot options admitted durable work")
	}
}

func TestUnitReplicationExplicitPushNeedsNoConfigurationAndCannotInheritCredentials(t *testing.T) {
	w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
	must(t, w.service.Update(ctx, "local", replication.Input{Username: ptr("saved-user"), Password: ptr("saved-lab-password")}))
	saved := stored(t, w, "local")
	configured, err := coordinator.Execute(ctx, "local", "")
	must(t, err)
	for _, url := range []string{saved.URL, "https://different.example/artifactory/destination"} {
		run, err := coordinator.ExecuteWithOptions(ctx, "local", "selected", replication.ExecutionInput{URL: &url})
		must(t, err)
		options, err := db.Executions().Options(ctx, run.Reference)
		must(t, err)
		if !options.Independent || options.Username != "" || options.Password != "" || options.RepositoryKey != "local" || options.TargetKey != run.Binding.TargetKey {
			t.Fatal("explicit connection inherited a saved identity or lost its immutable binding")
		}
		if url == saved.URL && run.Binding.TargetLeaseKey != configured.Binding.TargetLeaseKey {
			t.Fatal("manual and configured connections to the same endpoint have different exclusion identities")
		}
	}
	if !reflect.DeepEqual(saved, stored(t, w, "local")) {
		t.Fatal("explicit connection changed the saved configuration")
	}
	must(t, w.service.Delete(ctx, "local", ""))
	run, err := coordinator.ExecuteWithOptions(ctx, "local", "selected", replication.ExecutionInput{URL: ptr("https://different.example/artifactory/destination")})
	must(t, err)
	options, err := db.Executions().Options(ctx, run.Reference)
	must(t, err)
	if !options.Independent {
		t.Fatal("explicit push depended on a saved configuration")
	}
	rows, err := db.Replications().ForRepo(ctx, "local")
	must(t, err)
	if len(rows) != 0 {
		t.Fatal("explicit push invented a configuration row")
	}
}
