package model_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitExecutionRetentionCannotInventMutationEvidence(t *testing.T) {
	run := executionRun(t)
	effect := model.ExecutionEffect{
		Run: run.Reference, TransferID: run.Binding.TransferID, TransferIncarnation: run.Binding.TransferIncarnation,
		OperationKey: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64),
		Path: "selected/retained.bin", Version: 1, Outcome: model.EffectRetained, Updated: time.Unix(101, 0),
	}
	if err := effect.Validate(run); err != nil || effect.Count() != (model.ExecutionOutcomes{Succeeded: 1}) {
		t.Fatal("verified retention was not counted as a completed benign intent", err)
	}
	for _, change := range []func(*model.ExecutionEffect){
		func(value *model.ExecutionEffect) { value.Paths = []string{value.Path} },
		func(value *model.ExecutionEffect) { value.Reason = "source_changed" },
		func(value *model.ExecutionEffect) { value.Outcome = model.EffectSucceeded },
	} {
		invalid := effect
		change(&invalid)
		if err := invalid.Validate(run); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("retention accepted contradictory mutation or refusal evidence", err)
		}
	}
}
