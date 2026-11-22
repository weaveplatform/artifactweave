package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func effectHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func effectFor(run model.ExecutionRun, path string) model.ExecutionEffect {
	return model.ExecutionEffect{
		Run: run.Reference, TransferID: run.Binding.TransferID, TransferIncarnation: run.Binding.TransferIncarnation,
		OperationKey: effectHash("operation:" + path), Fingerprint: effectHash("intent:" + path),
		Version: 1, Path: path, Outcome: model.EffectPending, Updated: storekit.At(2),
	}
}

func recordEffect(t *testing.T, db port.Store, effect model.ExecutionEffect) {
	t.Helper()
	storekit.Must(t, db.Tx(t.Context(), func(ctx context.Context) error { return db.Executions().RecordEffect(ctx, effect) }))
}

func executionEffects(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "run", executionBinding(""), storekit.At(1)))
	effect := effectFor(run, "selected/a")
	storekit.WantErr(t, progress.RecordEffect(ctx, effect), kernel.ErrInvalid)
	recordEffect(t, db, effect)
	selected := storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference))
	storekit.Same(t, "selected intent is pending", model.ExecutionOutcomes{Pending: 1}, selected.Outcomes)
	recordEffect(t, db, effect)
	storekit.Same(t, "identical selection replay does not count twice", selected, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	complete := effect
	complete.Version++
	complete.Outcome, complete.Paths, complete.Updated = model.EffectSucceeded, []string{effect.Path, effect.Path + ".sha256"}, storekit.At(3)
	recordEffect(t, db, complete)
	retained := storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference))
	storekit.Same(t, "exact one selected success", model.ExecutionOutcomes{Succeeded: 1}, retained.Outcomes)
	storekit.Same(t, "retained exact effect proof", complete, storekit.Get[model.ExecutionEffect](t)(progress.Effect(ctx, run.Reference, effect.OperationKey)))
	recordEffect(t, db, complete)
	storekit.Same(t, "successful replay does not count companions twice", retained, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	for _, changed := range []model.ExecutionEffect{effect, func() model.ExecutionEffect {
		changed := complete
		changed.Fingerprint = effectHash("different-intent")
		return changed
	}()} {
		storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.RecordEffect(ctx, changed) }), kernel.ErrConflict)
	}
	rollback := effectFor(run, "selected/b")
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := progress.RecordEffect(ctx, rollback); err != nil {
			return fmt.Errorf("record rollback fixture effect: %w", err)
		}
		return errExecutionRollback
	})
	storekit.WantErr(t, err, errExecutionRollback)
	_, err = progress.Effect(ctx, run.Reference, rollback.OperationKey)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(t, "rolled back effect retains counters", retained, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	borrowed := effectFor(run, "selected/foreign")
	borrowed.TransferIncarnation = "another-transfer"
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.RecordEffect(ctx, borrowed) }), kernel.ErrInvalid)
}

func executionScopedStatus(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	target := model.Replication{Key: "target", RepoKey: "source", Revision: "configuration", URL: "https://peer.example/artifactory/destination", Password: "sealed-private"}
	storekit.Must(t, db.Replications().Put(ctx, target))
	run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "first", executionBinding(""), storekit.At(1)))
	for path, outcome := range map[string]model.EffectOutcome{
		"a/file": model.EffectSucceeded, "A/file": model.EffectFailed, "ab/file": model.EffectSkipped,
		"percent%_name/file": model.EffectSucceeded, "percentXXname/file": model.EffectFailed, "é/file": model.EffectSucceeded,
		"retained/file": model.EffectRetained,
	} {
		effect := effectFor(run, path)
		effect.Outcome = outcome
		if outcome == model.EffectSucceeded {
			effect.Paths = []string{path, path + ".sha256"}
		} else if outcome != model.EffectRetained {
			effect.Reason = "qualified_failure"
		}
		recordEffect(t, db, effect)
	}
	retained := storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference))
	complete := retained
	complete.Version++
	complete.Phase, complete.Completed, complete.Updated = model.ExecutionComplete, storekit.At(4), storekit.At(4)
	storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, retained.Version, complete) }))
	for _, test := range []struct {
		path string
		want model.ReplicationStatus
	}{
		{"", model.ReplicationPartialFailure},
		{"a", model.ReplicationOK},
		{"A", model.ReplicationError},
		{"ab", model.ReplicationWarn},
		{"percent%_name", model.ReplicationOK},
		{"é", model.ReplicationOK},
		{"retained", model.ReplicationOK},
		{"a/file.sha256", model.ReplicationOK},
	} {
		state := storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "repository-lifetime", test.path))
		storekit.Same(t, "exact scoped outcome "+test.path, test.want, state.Latest.Status)
		storekit.Same(t, "scoped prior completion "+test.path, storekit.At(4), state.LastCompleted)
	}
	for _, path := range []string{"absent", "percent", "a/f", "É"} {
		state := storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "repository-lifetime", path))
		storekit.Same(t, "no invented folder evidence "+path, model.ExecutionPathState{}, state)
	}
	second := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "second", executionBinding(""), storekit.At(5)))
	newEffect := effectFor(second, "outside/unrelated")
	newEffect.Updated = storekit.At(5)
	recordEffect(t, db, newEffect)
	state := storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "repository-lifetime", "a"))
	storekit.Same(t, "unrelated latest run cannot replace folder evidence", model.ReplicationOK, state.Latest.Status)
	newEffect = effectFor(second, "a/unfinished")
	newEffect.Updated = storekit.At(5)
	recordEffect(t, db, newEffect)
	state = storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "repository-lifetime", "a"))
	storekit.Same(t, "matching unfinished selection is incomplete", model.ReplicationIncomplete, state.Latest.Status)
	storekit.Same(t, "incomplete run keeps prior scoped completion", storekit.At(4), state.LastCompleted)
	target.Revision = "edited-credentials"
	storekit.Must(t, db.Replications().Put(ctx, target))
	storekit.Same(t, "ordinary config edit preserves history", state,
		storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "repository-lifetime", "a")))
	storekit.Same(t, "new repository lifetime has no old folder evidence", model.ExecutionPathState{},
		storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "recreated-repository", "a")))
	storekit.Must(t, db.Replications().Delete(ctx, target.Key))
	storekit.Same(t, "removed target has no current status", model.ExecutionPathState{},
		storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "repository-lifetime", "a")))
	target.Key = "new-target"
	storekit.Must(t, db.Replications().Put(ctx, target))
	storekit.Same(t, "recreated target does not borrow old execution", model.ExecutionPathState{},
		storekit.Get[model.ExecutionPathState](t)(progress.PathStatus(ctx, target.Key, "repository-lifetime", "a")))
	_, err := progress.Effect(ctx, second.Reference, effectHash("missing"))
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("missing effect returned different proof", err)
	}
}
