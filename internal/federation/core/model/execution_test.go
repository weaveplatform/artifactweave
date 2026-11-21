package model_test

import (
	"errors"
	"math"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func executionRun(t *testing.T) model.ExecutionRun {
	t.Helper()
	run, err := model.NewExecutionRun("logical-run", model.ExecutionBinding{
		TargetKey: "target", TargetRevision: "current-configuration",
		Repository: artifactsapi.TransferRepository{Key: "source", Incarnation: "repository-lifetime", Generation: "generation", PackageType: repo.Generic},
		Direction:  model.ReplicationPush, Credential: kernel.CredentialReference{Name: "author", TokenID: "token-reference"},
		TransferID: "transfer", TransferIncarnation: "transfer-lifetime",
	}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestUnitExecutionStatusNeedsCompleteActualOutcomes(t *testing.T) {
	for _, test := range []struct {
		name     string
		phase    model.ExecutionPhase
		outcomes model.ExecutionOutcomes
		want     model.ReplicationStatus
	}{
		{"queued", model.ExecutionPending, model.ExecutionOutcomes{}, model.ReplicationIncomplete},
		{"interrupted", model.ExecutionRunning, model.ExecutionOutcomes{Succeeded: 2, Pending: 1}, model.ReplicationIncomplete},
		{"all-success", model.ExecutionComplete, model.ExecutionOutcomes{Succeeded: 2}, model.ReplicationOK},
		{"empty-selection", model.ExecutionComplete, model.ExecutionOutcomes{}, model.ReplicationOK},
		{"mixed-effects", model.ExecutionComplete, model.ExecutionOutcomes{Succeeded: 1, Failed: 1}, model.ReplicationPartialFailure},
		{"all-failed", model.ExecutionComplete, model.ExecutionOutcomes{Failed: 1}, model.ReplicationError},
		{"skipped", model.ExecutionComplete, model.ExecutionOutcomes{Succeeded: 1, Skipped: 1}, model.ReplicationWarn},
		{"revoked-after-effect", model.ExecutionAborted, model.ExecutionOutcomes{Succeeded: 1, Pending: 1}, model.ReplicationPartialFailure},
		{"revoked-before-effect", model.ExecutionAborted, model.ExecutionOutcomes{}, model.ReplicationError},
		{"unfinished-success", model.ExecutionComplete, model.ExecutionOutcomes{Pending: 1}, model.ReplicationUnknown},
		{"negative-counter", model.ExecutionComplete, model.ExecutionOutcomes{Succeeded: -1}, model.ReplicationUnknown},
		{"overflow", model.ExecutionComplete, model.ExecutionOutcomes{Succeeded: math.MaxInt64, Failed: 1}, model.ReplicationUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := executionRun(t)
			run.Phase, run.Outcomes = test.phase, test.outcomes
			if test.phase == model.ExecutionComplete || test.phase == model.ExecutionAborted {
				run.Completed = run.Started
			}
			if got := run.Status(); got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
		})
	}
}

func TestUnitExecutionAdvanceCannotRebindOrEraseProvenEffects(t *testing.T) {
	previous := executionRun(t)
	previous.Phase, previous.Outcomes = model.ExecutionRunning, model.ExecutionOutcomes{Succeeded: 1, Pending: 1}
	next := previous
	next.Version++
	next.Phase, next.Completed, next.Outcomes = model.ExecutionComplete, next.Started, model.ExecutionOutcomes{Succeeded: 2}
	if err := model.ValidateExecutionAdvance(previous, next); err != nil {
		t.Fatal("valid recovered progress", err)
	}
	for _, edit := range []func(*model.ExecutionRun){
		func(run *model.ExecutionRun) { run.Reference.Incarnation = "another-lifetime" },
		func(run *model.ExecutionRun) { run.Binding.TargetRevision = "new-config" },
		func(run *model.ExecutionRun) { run.Binding.Repository.Incarnation = "recreated-source" },
		func(run *model.ExecutionRun) { run.Binding.Credential.TokenID = "different-token" },
		func(run *model.ExecutionRun) { run.Outcomes.Succeeded = 0 },
		func(run *model.ExecutionRun) { run.Version = previous.Version },
	} {
		changed := next
		edit(&changed)
		if err := model.ValidateExecutionAdvance(previous, changed); !errors.Is(err, kernel.ErrConflict) {
			t.Fatal("execution retry rebound or erased retained provenance", err)
		}
	}
	terminal := next
	next.Version++
	if err := model.ValidateExecutionAdvance(terminal, next); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("terminal execution was reopened", err)
	}
}
