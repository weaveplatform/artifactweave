package model

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// EffectOutcome describes one selected transfer intent, independently of the
// execution phase. Content verification alone remains pending when requested
// property synchronization has not completed.
type EffectOutcome string

// Retained selected-intent outcomes, with no upstream diagnostic text.
const (
	EffectPending   EffectOutcome = "pending"
	EffectSucceeded EffectOutcome = "succeeded"
	EffectRetained  EffectOutcome = "retained"
	EffectFailed    EffectOutcome = "failed"
	EffectSkipped   EffectOutcome = "skipped"
)

// ExecutionEffect ties selected and actual affected paths to the exact driver
// intent lifetime/version. Fingerprint is the immutable driver's intent hash.
// Paths contains only proven effects; Path is also indexed at initial selection
// so a selected unfinished folder can truthfully report incomplete.
type ExecutionEffect struct {
	Run                 ExecutionReference `json:"run"`
	TransferID          string             `json:"transfer_id"`
	TransferIncarnation string             `json:"transfer_incarnation"`
	OperationKey        string             `json:"operation_key"`
	Fingerprint         string             `json:"fingerprint"`
	Version             int64              `json:"version"`
	Path                string             `json:"path"`
	Outcome             EffectOutcome      `json:"outcome"`
	Reason              string             `json:"reason"`
	Paths               []string           `json:"paths"`
	Updated             time.Time          `json:"updated"`
}

// Validate refuses borrowed run/transfer lifetimes and invented success facts.
func (e ExecutionEffect) Validate(run ExecutionRun) error {
	if e.Run != run.Reference || e.TransferID != run.Binding.TransferID || e.TransferIncarnation != run.Binding.TransferIncarnation ||
		!pushDigest(e.OperationKey) || !pushDigest(e.Fingerprint) || e.Version < 1 || e.Updated.IsZero() ||
		!canonicalPushItemPath(e.Path) || !effectWithin(run.Binding.Root, e.Path) || len(e.Paths) > 65 {
		return fmt.Errorf("invalid replication effect identity: %w", kernel.ErrInvalid)
	}
	seen := map[string]bool{}
	for _, path := range e.Paths {
		if !canonicalPushItemPath(path) || !effectWithin(run.Binding.Root, path) || seen[path] {
			return fmt.Errorf("invalid replication effect path: %w", kernel.ErrInvalid)
		}
		seen[path] = true
	}
	switch e.Outcome {
	case EffectPending:
		if e.Reason != "" {
			return fmt.Errorf("unfinished effect has a terminal reason: %w", kernel.ErrInvalid)
		}
	case EffectSucceeded:
		if e.Reason != "" || !slices.Contains(e.Paths, e.Path) {
			return fmt.Errorf("replication success has no actual path evidence: %w", kernel.ErrInvalid)
		}
	case EffectRetained:
		if e.Reason != "" || len(e.Paths) != 0 {
			return fmt.Errorf("replication retention carries mutation evidence: %w", kernel.ErrInvalid)
		}
	case EffectFailed, EffectSkipped:
		if !pushReason(e.Reason) {
			return fmt.Errorf("replication effect reason is not sanitized: %w", kernel.ErrInvalid)
		}
	default:
		return fmt.Errorf("invalid replication effect outcome: %w", kernel.ErrInvalid)
	}
	return nil
}

func effectWithin(root, path string) bool {
	return root == "" || path == root || strings.HasPrefix(path, root+"/")
}

// Count contributes exactly one selected intent, regardless of companion paths.
func (e ExecutionEffect) Count() ExecutionOutcomes {
	switch e.Outcome {
	case EffectSucceeded, EffectRetained:
		return ExecutionOutcomes{Succeeded: 1}
	case EffectFailed:
		return ExecutionOutcomes{Failed: 1}
	case EffectSkipped:
		return ExecutionOutcomes{Skipped: 1}
	default:
		return ExecutionOutcomes{Pending: 1}
	}
}

// ApplyExecutionEffect updates cumulative counters in constant time. Replaying
// identical proof is a no-op; stale/contradictory evidence cannot erase a newer
// observation. The store commits this result with the effect and final fence.
func ApplyExecutionEffect(run ExecutionRun, previous *ExecutionEffect, next ExecutionEffect) (ExecutionRun, bool, error) {
	if err := next.Validate(run); err != nil {
		return ExecutionRun{}, false, err
	}
	old := ExecutionOutcomes{}
	if previous != nil {
		if err := previous.Validate(run); err != nil {
			return ExecutionRun{}, false, fmt.Errorf("stored replication effect invalid: %w", kernel.ErrStore)
		}
		if reflect.DeepEqual(*previous, next) {
			return run, false, nil
		}
		if previous.Run != next.Run || previous.TransferID != next.TransferID || previous.TransferIncarnation != next.TransferIncarnation ||
			previous.OperationKey != next.OperationKey || previous.Fingerprint != next.Fingerprint || previous.Path != next.Path ||
			previous.Outcome != EffectPending || next.Version <= previous.Version || next.Updated.Before(previous.Updated) {
			return ExecutionRun{}, false, fmt.Errorf("replication effect changed or regressed: %w", kernel.ErrConflict)
		}
		old = previous.Count()
	}
	if run.Validate() != nil || run.Phase == ExecutionComplete || run.Phase == ExecutionAborted || run.Version == math.MaxInt64 {
		return ExecutionRun{}, false, fmt.Errorf("replication execution no longer accepts effects: %w", kernel.ErrConflict)
	}
	if run.Outcomes.Succeeded < old.Succeeded || run.Outcomes.Failed < old.Failed || run.Outcomes.Skipped < old.Skipped || run.Outcomes.Pending < old.Pending {
		return ExecutionRun{}, false, fmt.Errorf("replication counters disagree with retained effects: %w", kernel.ErrStore)
	}
	delta := next.Count()
	out := run
	out.Version++
	out.Phase = ExecutionRunning
	out.Updated = maxTime(run.Updated, next.Updated)
	out.Outcomes = ExecutionOutcomes{
		Succeeded: run.Outcomes.Succeeded - old.Succeeded + delta.Succeeded,
		Failed:    run.Outcomes.Failed - old.Failed + delta.Failed,
		Skipped:   run.Outcomes.Skipped - old.Skipped + delta.Skipped,
		Pending:   run.Outcomes.Pending - old.Pending + delta.Pending,
	}
	if err := ValidateExecutionAdvance(run, out); err != nil {
		return ExecutionRun{}, false, err
	}
	return out, true, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ExecutionPathState is a secret-free scoped status projection. Its latest run
// must have selected or actually affected this path, regardless of Binding.Root.
type ExecutionPathState struct {
	Latest        ReplicationRun
	LastCompleted time.Time
}
