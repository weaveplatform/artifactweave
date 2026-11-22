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

func TestUnitReplicationOneShotInvalidScopeAndDifferentDestinationDoNotEnqueue(t *testing.T) {
	_, db, coordinator, ctx := coordinatorFixture(t, "local", false)
	for _, input := range []replication.ExecutionInput{
		{URL: ptr("https://different.example/artifactory/target")},
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
