package replication_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type statusFaultStore struct {
	replication.StatusStore
	targets   port.ReplicationStore
	progress  port.ExecutionProgressStore
	afterRead bool
}

const statusCredentialMarker = "status-corrupt-endpoint-credential-must-not-escape"

func (s statusFaultStore) Replications() port.ReplicationStore     { return s.targets }
func (s statusFaultStore) Executions() port.ExecutionProgressStore { return s.progress }
func (s statusFaultStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	return s.StatusStore.Tx(ctx, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		if s.afterRead {
			return errExecutionStorage
		}
		return nil
	})
}

type statusFaultTargets struct {
	port.ReplicationStore
	readError bool
	duplicate bool
}

func (p statusFaultTargets) StatusTargets(ctx context.Context, repository string, page kernel.Page) ([]model.ReplicationTargetState, string, error) {
	if p.readError {
		return nil, "", errExecutionStorage
	}
	targets, next, err := p.ReplicationStore.StatusTargets(ctx, repository, page)
	if err == nil && p.duplicate {
		targets = append(targets, targets...)
	}
	return targets, next, err
}

type statusFaultProgress struct {
	port.ExecutionProgressStore
	stage         string
	explicitCalls int
}

func (p *statusFaultProgress) PathStatus(ctx context.Context, key, incarnation, root string) (model.ExecutionPathState, error) {
	if p.stage == "saved history" || p.stage == "explicit history" && len(key) > 9 && key[:9] == "explicit:" {
		return model.ExecutionPathState{}, errExecutionStorage
	}
	return p.ExecutionProgressStore.PathStatus(ctx, key, incarnation, root)
}

func (p *statusFaultProgress) ExplicitTargets(ctx context.Context, repository, incarnation string, page kernel.Page) ([]model.ReplicationTargetState, string, error) {
	p.explicitCalls++
	if p.stage == "explicit targets" || p.stage == "later explicit page" && page.After != "" {
		return nil, "", errExecutionStorage
	}
	targets, next, err := p.ExecutionProgressStore.ExplicitTargets(ctx, repository, incarnation, page)
	if err == nil && p.stage == "invalid explicit endpoint" && len(targets) > 0 {
		targets[0].URL = "https://credential:" + statusCredentialMarker + "@peer.invalid/artifactory/repo"
	}
	if err == nil && p.stage == "later explicit page" {
		next = "opaque-next-page"
	}
	return targets, next, err
}

func TestUnitReplicationStatusRefusesPartialSnapshotsAndRecovers(t *testing.T) {
	for _, stage := range []string{"authorization", "repository class", "repository lifetime", "saved targets", "saved history", "multiple saved targets", "explicit targets", "invalid explicit endpoint", "explicit history", "later explicit page", "snapshot settlement"} {
		t.Run(stage, func(t *testing.T) {
			_, db, coordinator, ctx := coordinatorFixture(t, "local", false)
			_, err := coordinator.Execute(ctx, "local", "selected")
			must(t, err)
			_, err = coordinator.ExecuteWithOptions(ctx, "local", "selected", replication.ExecutionInput{URL: ptr("https://other.example/artifactory/destination")})
			must(t, err)
			reader := replication.StatusReader{Store: db, Authorize: func(ctx context.Context, repository, root string) (repo.Class, string, error) {
				if !db.InTx(ctx) || repository != "local" || root != "selected" {
					t.Fatal("scoped read escaped its current snapshot")
				}
				return repo.Local, "repository-lifetime", nil
			}}
			before, err := reader.Read(ctx, "local", "selected")
			must(t, err)
			if len(before.Targets) != 2 {
				t.Fatal("control omitted saved or explicit execution history")
			}
			originalAuthorize := reader.Authorize
			targets := statusFaultTargets{ReplicationStore: db.Replications(), readError: stage == "saved targets", duplicate: stage == "multiple saved targets"}
			progress := &statusFaultProgress{ExecutionProgressStore: db.Executions(), stage: stage}
			reader.Store = statusFaultStore{StatusStore: db, targets: targets, progress: progress, afterRead: stage == "snapshot settlement"}
			want := error(kernel.ErrStore)
			switch stage {
			case "authorization":
				want = kernel.ErrForbidden
				reader.Authorize = func(context.Context, string, string) (repo.Class, string, error) { return "", "", kernel.ErrForbidden }
			case "repository class":
				want = kernel.ErrInvalid
				reader.Authorize = func(context.Context, string, string) (repo.Class, string, error) {
					return repo.Virtual, "repository-lifetime", nil
				}
			case "repository lifetime":
				reader.Authorize = func(context.Context, string, string) (repo.Class, string, error) { return repo.Local, "", nil }
			case "multiple saved targets":
				want = replication.ErrMultipleTargets
			}
			got, err := reader.Read(ctx, "local", "selected")
			if !errors.Is(err, want) || !reflect.DeepEqual(got, replication.StatusView{}) || strings.Contains(err.Error(), statusCredentialMarker) {
				t.Fatal("failed scoped read disclosed a partial target/history view or credential", got, err)
			}
			if stage == "later explicit page" && progress.explicitCalls != 2 {
				t.Fatal("later-page fault did not follow a successful history read")
			}
			reader.Store, reader.Authorize = db, originalAuthorize
			recovered, err := reader.Read(ctx, "local", "selected")
			must(t, err)
			if !reflect.DeepEqual(recovered, before) {
				t.Fatal("refused status read changed retained evidence", recovered)
			}
		})
	}
}

func TestUnitReplicationStatusGroupsEquivalentDestinationsWithoutLosingCompletion(t *testing.T) {
	for _, scenario := range []string{"newer explicit pending", "older explicit completion", "simultaneous pending"} {
		t.Run(scenario, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, "local", false)
			saved := stored(t, w, "local")
			savedAt, explicitAt := time.Unix(1, 0), time.Unix(3, 0)
			if scenario == "older explicit completion" {
				savedAt, explicitAt = explicitAt, savedAt
			}
			if scenario == "simultaneous pending" {
				explicitAt = savedAt
			}
			coordinator.Now = func() time.Time { return savedAt }
			configured, err := coordinator.Execute(ctx, "local", "selected")
			must(t, err)
			coordinator.Now = func() time.Time { return explicitAt }
			explicit, err := coordinator.ExecuteWithOptions(ctx, "local", "selected", replication.ExecutionInput{URL: &saved.URL})
			must(t, err)
			completed := configured
			pending := explicit
			if scenario == "older explicit completion" {
				completed, pending = explicit, configured
			}
			completed.Version++
			completed.Phase = model.ExecutionComplete
			completed.Updated, completed.Completed = completed.Started.Add(time.Second), completed.Started.Add(time.Second)
			must(t, db.Tx(ctx, func(ctx context.Context) error { return db.Executions().Advance(ctx, completed.Version-1, completed) }))
			reader := replication.StatusReader{Store: db, Authorize: func(context.Context, string, string) (repo.Class, string, error) {
				return repo.Local, "repository-lifetime", nil
			}}
			view, err := reader.Read(ctx, "local", "")
			must(t, err)
			if len(view.Targets) != 1 || view.Targets[0].URL != saved.URL || view.Targets[0].Status != model.ReplicationIncomplete ||
				!view.Targets[0].LatestStarted.Equal(pending.Started) ||
				!view.Targets[0].LastCompleted.Equal(completed.Completed) {
				t.Fatal("destination grouping lost current incompleteness or prior completion", view)
			}
		})
	}
}

func TestUnitReplicationNeverRunStatusPreservesLegacyEndpointConfiguration(t *testing.T) {
	w, db, _, ctx := coordinatorFixture(t, "local", false)
	target := stored(t, w, "local")
	target.URL = "https://legacy.example"
	must(t, db.Replications().Put(ctx, target))
	reader := replication.StatusReader{Store: db, Authorize: func(context.Context, string, string) (repo.Class, string, error) {
		return repo.Local, "repository-lifetime", nil
	}}
	view, err := reader.Read(ctx, "local", "")
	must(t, err)
	if len(view.Targets) != 1 || view.Targets[0].URL != target.URL || view.Targets[0].Status != model.ReplicationNeverRun {
		t.Fatal("never-run legacy endpoint was rewritten or given invented execution history", view)
	}
}

func TestUnitReplicationStatusRejectsEscapingScopeBeforeAuthorityOrHistory(t *testing.T) {
	_, db, _, ctx := coordinatorFixture(t, "local", false)
	read := false
	reader := replication.StatusReader{Store: db, Authorize: func(context.Context, string, string) (repo.Class, string, error) {
		read = true
		return repo.Local, "repository-lifetime", nil
	}}
	view, err := reader.Read(ctx, "local", "../private")
	if !errors.Is(err, kernel.ErrInvalid) || read || !reflect.DeepEqual(view, replication.StatusView{}) {
		t.Fatal("escaping status scope reached authority or retained history", err)
	}
}
