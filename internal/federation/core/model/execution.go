package model

import (
	"crypto/rand"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ExecutionReference identifies a logical dispatch independently of a queue
// attempt, a target's public key, and its separate push/pull progress lifetime.
type ExecutionReference struct {
	ID          string `json:"id"`
	Incarnation string `json:"incarnation"`
}

// ExecutionBinding retains private provenance for manual or scheduled dispatch.
// Occurrence is empty for manual work and the Quartz trigger ID otherwise. The
// transfer reference links actual retained progress, never a public timestamp.
type ExecutionBinding struct {
	TargetKey           string                          `json:"target_key"`
	TargetRevision      string                          `json:"target_revision"`
	Repository          artifactsapi.TransferRepository `json:"repository"`
	Direction           ReplicationDirection            `json:"direction"`
	Root                string                          `json:"root"`
	Credential          kernel.CredentialReference      `json:"credential"`
	Occurrence          string                          `json:"occurrence"`
	TransferID          string                          `json:"transfer_id"`
	TransferIncarnation string                          `json:"transfer_incarnation"`
	OneShot             bool                            `json:"one_shot,omitempty"`
}

// ExecutionPhase is dispatch lifecycle state. Transfer progress remains owned
// by the direction driver and can survive interruption of a queue attempt.
type ExecutionPhase string

// ExecutionFailure is a closed private diagnostic. Raw peer, credential and
// database error strings must never enter durable execution or queue records.
type ExecutionFailure string

// Closed execution failure codes, independent of raw transport diagnostics.
const (
	ExecutionRetryExhausted       ExecutionFailure = "retry_exhausted"
	ExecutionTransferFailed       ExecutionFailure = "transfer_failed"
	ExecutionAuthorityDenied      ExecutionFailure = "authority_denied"
	ExecutionConfigurationChanged ExecutionFailure = "configuration_changed"
	ExecutionSourceOffline        ExecutionFailure = "source_offline"
	ExecutionNotStored            ExecutionFailure = "source_not_stored"
	ExecutionIntegrityFailed      ExecutionFailure = "integrity_failed"
	ExecutionUnsupported          ExecutionFailure = "unsupported"
)

func (f ExecutionFailure) valid() bool {
	switch f {
	case ExecutionRetryExhausted, ExecutionTransferFailed, ExecutionAuthorityDenied, ExecutionConfigurationChanged,
		ExecutionSourceOffline, ExecutionNotStored, ExecutionIntegrityFailed, ExecutionUnsupported:
		return true
	default:
		return false
	}
}

// Durable logical dispatch phases, independent of queue attempt generations.
const (
	ExecutionPending  ExecutionPhase = "pending"
	ExecutionRunning  ExecutionPhase = "running"
	ExecutionComplete ExecutionPhase = "complete"
	ExecutionAborted  ExecutionPhase = "aborted"
)

// ExecutionOutcomes counts actual selected intents, including verified benign
// retention during reconciliation. Pending includes uncertain
// or unfinished effects until the direction driver resolves them; an incomplete
// traversal or an uncertain transfer cannot be counted as successful.
type ExecutionOutcomes struct {
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Skipped   int64 `json:"skipped"`
	Pending   int64 `json:"pending"`
}

// ExecutionRun links durable dispatch to transfer evidence. No passwords,
// copied permission decisions or source URLs belong in this private record.
type ExecutionRun struct {
	Reference ExecutionReference `json:"reference"`
	Binding   ExecutionBinding   `json:"binding"`
	Version   int64              `json:"version"`
	Phase     ExecutionPhase     `json:"phase"`
	Failure   ExecutionFailure   `json:"failure"`
	Outcomes  ExecutionOutcomes  `json:"outcomes"`
	Started   time.Time          `json:"started"`
	Updated   time.Time          `json:"updated"`
	Completed time.Time          `json:"completed"`
}

// Validate bounds the identity used by private progress and queue linkage.
func (r ExecutionReference) Validate() error {
	if !nonemptyText(r.ID) || !nonemptyText(r.Incarnation) || len(r.ID) > 256 || len(r.Incarnation) > 256 {
		return fmt.Errorf("invalid execution reference: %w", kernel.ErrInvalid)
	}
	return nil
}

// Validate requires complete immutable provenance and a restorable credential.
func (b ExecutionBinding) Validate() error {
	for _, value := range []string{
		b.TargetKey, b.TargetRevision, b.Repository.Key, b.Repository.Incarnation,
		b.Repository.Generation, string(b.Repository.PackageType), b.TransferID, b.TransferIncarnation, b.Credential.Name,
	} {
		if !nonemptyText(value) {
			return fmt.Errorf("incomplete execution binding: %w", kernel.ErrInvalid)
		}
	}
	if b.OneShot && b.Occurrence != "" || len(b.TransferID) > 256 || len(b.TransferIncarnation) > 256 || !privateText(b.Root) || !privateText(b.Occurrence) ||
		b.Credential.Anonymous || b.Credential.Registry != nil || !privateText(b.Credential.TokenID) ||
		!b.Credential.Password && b.Credential.TokenID == "" ||
		b.Direction != ReplicationPush && b.Direction != ReplicationPull {
		return fmt.Errorf("invalid execution provenance: %w", kernel.ErrInvalid)
	}
	if b.Root != "" {
		_, _, _, err := kernel.SplitItemPath(b.Root)
		if err != nil || b.Root == "." || strings.Trim(b.Root, "/") != b.Root {
			return fmt.Errorf("invalid execution selection: %w", kernel.ErrInvalid)
		}
	}
	return nil
}

// Validate refuses negative and overflowing cumulative effect counts.
func (o ExecutionOutcomes) Validate() error {
	var total int64
	for _, value := range []int64{o.Succeeded, o.Failed, o.Skipped, o.Pending} {
		if value < 0 || total > math.MaxInt64-value {
			return fmt.Errorf("invalid execution outcomes: %w", kernel.ErrInvalid)
		}
		total += value
	}
	return nil
}

// NewExecutionRun creates a pending logical lifetime without queue ownership.
func NewExecutionRun(id string, binding ExecutionBinding, at time.Time) (ExecutionRun, error) {
	at = at.UTC().Truncate(time.Microsecond)
	run := ExecutionRun{
		Reference: ExecutionReference{ID: id, Incarnation: rand.Text()}, Binding: binding,
		Version: 1, Phase: ExecutionPending, Started: at, Updated: at,
	}
	return run, run.Validate()
}

// Validate checks lifecycle clocks and refuses completion with unfinished work.
func (r ExecutionRun) Validate() error {
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if err := r.Outcomes.Validate(); err != nil {
		return err
	}
	terminal := r.Phase == ExecutionComplete || r.Phase == ExecutionAborted
	if r.Phase == ExecutionAborted && !r.Failure.valid() || r.Phase != ExecutionAborted && r.Failure != "" {
		return fmt.Errorf("invalid execution failure code: %w", kernel.ErrInvalid)
	}
	if r.Version < 1 || r.Started.IsZero() || r.Updated.Before(r.Started) || terminal != !r.Completed.IsZero() ||
		terminal && (r.Completed.Before(r.Started) || r.Updated.Before(r.Completed)) {
		return fmt.Errorf("invalid execution lifecycle: %w", kernel.ErrInvalid)
	}
	switch r.Phase {
	case ExecutionPending, ExecutionRunning, ExecutionAborted:
	case ExecutionComplete:
		if r.Outcomes.Pending != 0 {
			return fmt.Errorf("execution completed with unfinished effects: %w", kernel.ErrInvalid)
		}
	default:
		return fmt.Errorf("invalid execution phase: %w", kernel.ErrInvalid)
	}
	return nil
}

// Status derives a public answer from proven lifecycle/outcomes. An empty
// completed selection is OK repository-wide; it establishes no folder effect.
func (r ExecutionRun) Status() ReplicationStatus {
	if r.Validate() != nil {
		return ReplicationUnknown
	}
	return r.Outcomes.Status(r.Phase)
}

// Status derives a scoped outcome without reading an execution's private
// binding. Status stores use it only after selecting exact retained evidence.
func (o ExecutionOutcomes) Status(phase ExecutionPhase) ReplicationStatus {
	if o.Validate() != nil || phase != ExecutionPending && phase != ExecutionRunning && phase != ExecutionComplete && phase != ExecutionAborted ||
		phase == ExecutionComplete && o.Pending != 0 {
		return ReplicationUnknown
	}
	if phase == ExecutionPending || phase == ExecutionRunning {
		return ReplicationIncomplete
	}
	if phase == ExecutionAborted || o.Failed > 0 {
		if o.Succeeded > 0 {
			return ReplicationPartialFailure
		}
		return ReplicationError
	}
	if o.Skipped > 0 {
		return ReplicationWarn
	}
	return ReplicationOK
}

// ValidateExecutionAdvance prevents a retry or expired dispatch from rebinding,
// reopening or downgrading retained evidence. The store still owns CAS and the
// worker must apply its final lease fence after every progress/status effect.
func ValidateExecutionAdvance(previous, next ExecutionRun) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if previous.Validate() != nil || previous.Version == math.MaxInt64 || next.Version != previous.Version+1 ||
		previous.Reference != next.Reference || !reflect.DeepEqual(previous.Binding, next.Binding) ||
		!previous.Started.Equal(next.Started) || next.Updated.Before(previous.Updated) ||
		previous.Phase == ExecutionComplete || previous.Phase == ExecutionAborted ||
		previous.Phase == ExecutionRunning && next.Phase == ExecutionPending ||
		next.Outcomes.Succeeded < previous.Outcomes.Succeeded || next.Outcomes.Failed < previous.Outcomes.Failed ||
		next.Outcomes.Skipped < previous.Outcomes.Skipped {
		return fmt.Errorf("stale or regressing execution progress: %w", kernel.ErrConflict)
	}
	return nil
}
