package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PushReference identifies one lifetime of a caller-stable logical run. Queue
// generations and wall-clock start times are not logical execution identities.
type PushReference struct {
	ID          string `json:"id"`
	Incarnation string `json:"incarnation"`
}

// PushBinding is immutable private execution provenance. It retains references,
// never a target password or a cached administrator/permission decision.
// TransportRevision identifies the resolved proxy/connection policy without
// exposing its credentials. Every attempt must refresh all current authority.
type PushBinding struct {
	TargetKey         string                          `json:"target_key"`
	TargetRevision    string                          `json:"target_revision"`
	TransportRevision string                          `json:"transport_revision"`
	Source            artifactsapi.TransferRepository `json:"source"`
	Credential        kernel.CredentialReference      `json:"credential"`
	Root              string                          `json:"root"`
	Include           string                          `json:"include"`
	Exclude           string                          `json:"exclude"`
	SyncDeletes       bool                            `json:"sync_deletes"`
	SyncProperties    bool                            `json:"sync_properties"`
	SyncStatistics    bool                            `json:"sync_statistics"`
}

// PushPhase is private durable traversal state, not a public replication status.
type PushPhase string

// Private traversal phases.
const (
	PushCopy      PushPhase = "copy"
	PushReconcile PushPhase = "reconcile"
	PushComplete  PushPhase = "complete"
	PushFailed    PushPhase = "failed"
)

// Terminal reports whether execution must refuse every further remote attempt.
func (p PushPhase) Terminal() bool { return p == PushComplete || p == PushFailed }

// PushRun preserves source and reconciliation positions independently. Done
// distinguishes an exhausted empty cursor from enumeration not yet started.
// Complete means traversal ended; item outcomes still determine public success.
type PushRun struct {
	Reference    PushReference `json:"reference"`
	Binding      PushBinding   `json:"binding"`
	Version      int64         `json:"version"`
	Phase        PushPhase     `json:"phase"`
	SourceCursor string        `json:"source_cursor"`
	TargetCursor string        `json:"target_cursor"`
	SourceDone   bool          `json:"source_done"`
	TargetDone   bool          `json:"target_done"`
	Started      time.Time     `json:"started"`
	Updated      time.Time     `json:"updated"`
	Completed    time.Time     `json:"completed"`
}

// NewPushRun creates only initial state. Adapters return the existing lifetime
// on an identical binding retry rather than replacing it with this new nonce.
func NewPushRun(id string, binding PushBinding, at time.Time) (PushRun, error) {
	at = at.UTC().Truncate(time.Microsecond)
	r := PushRun{
		Reference: PushReference{ID: id, Incarnation: rand.Text()}, Binding: binding,
		Version: 1, Phase: PushCopy, Started: at, Updated: at,
	}
	if err := r.Validate(); err != nil {
		return PushRun{}, err
	}
	return r, nil
}

func privateText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func nonemptyText(value string) bool { return value != "" && privateText(value) }

// Validate checks the bounded identity used in compound database keys.
func (r PushReference) Validate() error {
	if !nonemptyText(r.ID) || len(r.ID) > 256 || !nonemptyText(r.Incarnation) || len(r.Incarnation) > 256 {
		return fmt.Errorf("invalid push run reference: %w", kernel.ErrInvalid)
	}
	return nil
}

// Validate checks complete provenance without manufacturing current authority.
func (b PushBinding) Validate() error {
	for _, value := range []string{b.TargetKey, b.TargetRevision, b.TransportRevision, b.Source.Key, b.Source.Incarnation, b.Source.Generation, string(b.Source.PackageType), b.Credential.Name} {
		if !nonemptyText(value) {
			return fmt.Errorf("incomplete push run binding: %w", kernel.ErrInvalid)
		}
	}
	if b.Credential.Anonymous || b.Credential.Registry != nil || !privateText(b.Credential.TokenID) ||
		!b.Credential.Password && b.Credential.TokenID == "" {
		return fmt.Errorf("invalid push source credential reference: %w", kernel.ErrInvalid)
	}
	for _, value := range []string{b.Root, b.Include, b.Exclude} {
		if !privateText(value) {
			return fmt.Errorf("invalid push selection: %w", kernel.ErrInvalid)
		}
	}
	if b.Root != "" {
		if b.Root != strings.Trim(b.Root, "/") || b.Root == "." {
			return fmt.Errorf("noncanonical push root: %w", kernel.ErrInvalid)
		}
		if _, _, _, err := kernel.SplitItemPath(b.Root); err != nil {
			return fmt.Errorf("push root: %w: %w", kernel.ErrInvalid, err)
		}
	}
	return nil
}

// Validate checks phase, cursor and clock consistency. An adapter separately
// checks unfinished items before a traversal can move to its next phase.
func (r PushRun) Validate() error {
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if r.Version < 1 || r.Started.IsZero() || r.Updated.Before(r.Started) ||
		!privateText(r.SourceCursor) || !privateText(r.TargetCursor) ||
		r.SourceDone && r.SourceCursor != "" || r.TargetDone && r.TargetCursor != "" {
		return fmt.Errorf("invalid push progress: %w", kernel.ErrInvalid)
	}
	switch r.Phase {
	case PushCopy:
		if r.TargetCursor != "" || r.TargetDone {
			return fmt.Errorf("push reconciliation preceded copy: %w", kernel.ErrInvalid)
		}
	case PushReconcile:
		if !r.SourceDone || !r.Binding.SyncDeletes {
			return fmt.Errorf("push reconciliation has no completed source traversal: %w", kernel.ErrInvalid)
		}
	case PushComplete:
		if !r.SourceDone || r.Binding.SyncDeletes && !r.TargetDone {
			return fmt.Errorf("push completed before traversal: %w", kernel.ErrInvalid)
		}
	case PushFailed:
	default:
		return fmt.Errorf("unknown push phase: %w", kernel.ErrInvalid)
	}
	if r.Phase.Terminal() != !r.Completed.IsZero() || !r.Completed.IsZero() &&
		(r.Completed.Before(r.Started) || r.Updated.Before(r.Completed)) {
		return fmt.Errorf("invalid push terminal time: %w", kernel.ErrInvalid)
	}
	return nil
}

// ValidatePushAdvance rejects replay, rebinding, cursor reopening and phase
// regression before an adapter performs its expected-version update.
func ValidatePushAdvance(current, next PushRun) error {
	if err := current.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if current.Version == math.MaxInt64 || next.Version != current.Version+1 || current.Phase.Terminal() ||
		next.Reference != current.Reference || next.Binding != current.Binding || !next.Started.Equal(current.Started) ||
		next.Updated.Before(current.Updated) || current.SourceDone && !next.SourceDone || current.TargetDone && !next.TargetDone {
		return fmt.Errorf("push progress changed: %w", kernel.ErrConflict)
	}
	if current.Phase == PushReconcile && next.Phase == PushCopy {
		return fmt.Errorf("push phase regressed: %w", kernel.ErrConflict)
	}
	if current.Phase == PushCopy && next.Phase == PushComplete && current.Binding.SyncDeletes {
		return fmt.Errorf("push skipped reconciliation: %w", kernel.ErrConflict)
	}
	return nil
}

type (
	// PushAction identifies the durable mutation intent.
	PushAction string
	// PushItemPhase records monotonic destination verification evidence.
	PushItemPhase string
	// PushOutcome distinguishes verified success from skipped or failed work.
	PushOutcome string
)

// Private actions, evidence phases and terminal outcomes.
const (
	PushUpload PushAction = "upload"
	PushDelete PushAction = "delete"

	PushIntent           PushItemPhase = "intent"
	PushContentVerified  PushItemPhase = "content_verified"
	PushMetadataVerified PushItemPhase = "metadata_verified"
	PushItemComplete     PushItemPhase = "complete"

	PushSucceeded  PushOutcome = "succeeded"
	PushSkipped    PushOutcome = "skipped"
	PushItemFailed PushOutcome = "failed"
)

// PushTargetSnapshot is secret-free destination evidence for retry decisions.
// A delete intent never authorizes recursively deleting an unselected subtree.
type PushTargetSnapshot struct {
	Actual   kernel.Checksums `json:"actual"`
	Size     int64            `json:"size"`
	Folder   bool             `json:"folder"`
	Modified time.Time        `json:"modified"`
	Updated  time.Time        `json:"updated"`
}

// PushItem persists intent before remote I/O. An Intent may already have taken
// effect remotely after a lost acknowledgement: retry must inspect the target,
// not infer that the mutation did not happen. Outcomes are private reason codes,
// never raw remote bodies, URLs, headers or passwords.
type PushItem struct {
	Run            PushReference              `json:"run"`
	Action         PushAction                 `json:"action"`
	Path           string                     `json:"path"`
	Source         *artifactsapi.TransferItem `json:"source"`
	Target         *PushTargetSnapshot        `json:"target"`
	Version        int64                      `json:"version"`
	Phase          PushItemPhase              `json:"phase"`
	Outcome        PushOutcome                `json:"outcome"`
	Reason         string                     `json:"reason"`
	VerifiedTarget *PushTargetSnapshot        `json:"verified_target"`
	TargetAbsent   bool                       `json:"target_absent"`
	Updated        time.Time                  `json:"updated"`
}

func canonicalPushItemPath(path string) bool {
	if !nonemptyText(path) || path != strings.Trim(path, "/") || path == "." {
		return false
	}
	_, _, _, err := kernel.SplitItemPath(path)
	return err == nil
}

func pushDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func pushTargetValid(target *PushTargetSnapshot) bool {
	return target != nil && target.Size >= 0 && (target.Folder && target.Size == 0 || !target.Folder && pushDigest(target.Actual.SHA256))
}

func pushReason(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func (i PushItem) uploadProof() bool {
	return i.Source != nil && pushTargetValid(i.VerifiedTarget) && i.VerifiedTarget.Folder == i.Source.Folder &&
		(i.Source.Folder || i.VerifiedTarget.Actual.SHA256 == i.Source.Actual.SHA256 && i.VerifiedTarget.Size == i.Source.Size)
}

// Validate checks represented facts and success evidence. It grants no source
// Read or remote mutation permission; execution must still refresh both.
func (i PushItem) Validate(run PushRun) error {
	if err := i.Run.Validate(); err != nil {
		return err
	}
	if i.Run != run.Reference || i.Version < 1 || !canonicalPushItemPath(i.Path) || i.Updated.IsZero() {
		return fmt.Errorf("invalid push item reference: %w", kernel.ErrInvalid)
	}
	if root := run.Binding.Root; root != "" && i.Path != root && !strings.HasPrefix(i.Path, root+"/") {
		return fmt.Errorf("push item escaped its source root: %w", kernel.ErrInvalid)
	}
	if i.Target != nil && !pushTargetValid(i.Target) {
		return fmt.Errorf("invalid push destination snapshot: %w", kernel.ErrInvalid)
	}
	switch i.Action {
	case PushUpload:
		if i.Source == nil || i.Source.ID <= 0 || i.Source.Path != i.Path || i.Source.Repository != run.Binding.Source ||
			!pushDigest(i.Source.Version) || i.Source.Size < 0 || i.Source.Folder && i.Source.Size != 0 ||
			!i.Source.Folder && !pushDigest(i.Source.Actual.SHA256) || i.Source.PropertiesIncluded != run.Binding.SyncProperties ||
			(i.Source.Statistics != nil) != (run.Binding.SyncStatistics && !i.Source.Folder) {
			return fmt.Errorf("invalid push source snapshot: %w", kernel.ErrInvalid)
		}
	case PushDelete:
		if i.Source != nil || i.Target == nil || !run.Binding.SyncDeletes {
			return fmt.Errorf("invalid push delete intent: %w", kernel.ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown push action: %w", kernel.ErrInvalid)
	}
	return i.validateEvidence()
}

// ValidatePushItemCreation restricts a fresh row to an unexecuted intent in the
// current traversal. Existing identical intents are read before this check.
func ValidatePushItemCreation(run PushRun, item PushItem) error {
	if err := item.Validate(run); err != nil {
		return err
	}
	if run.Phase.Terminal() || item.Version != 1 || item.Phase != PushIntent || item.VerifiedTarget != nil || item.TargetAbsent {
		return fmt.Errorf("push item is not a fresh intent: %w", kernel.ErrConflict)
	}
	if item.Action == PushUpload && (run.Phase != PushCopy || run.SourceDone) ||
		item.Action == PushDelete && (run.Phase != PushReconcile || run.TargetDone) {
		return fmt.Errorf("push traversal no longer admits this intent: %w", kernel.ErrConflict)
	}
	return nil
}

func (i PushItem) validateEvidence() error {
	switch i.Phase {
	case PushIntent:
	case PushContentVerified, PushMetadataVerified:
		if i.Action != PushUpload || !i.uploadProof() {
			return fmt.Errorf("push content has no matching proof: %w", kernel.ErrInvalid)
		}
	case PushItemComplete:
		switch i.Outcome {
		case PushSucceeded:
			if i.Reason != "" || i.Action == PushUpload && !i.uploadProof() || i.Action == PushDelete && !i.TargetAbsent {
				return fmt.Errorf("push success has no matching proof: %w", kernel.ErrInvalid)
			}
		case PushSkipped, PushItemFailed:
			if !pushReason(i.Reason) {
				return fmt.Errorf("push outcome needs a safe reason: %w", kernel.ErrInvalid)
			}
		default:
			return fmt.Errorf("unknown push outcome: %w", kernel.ErrInvalid)
		}
		return nil
	default:
		return fmt.Errorf("unknown push item phase: %w", kernel.ErrInvalid)
	}
	if i.Outcome != "" || i.Reason != "" {
		return fmt.Errorf("unfinished push item has a terminal outcome: %w", kernel.ErrInvalid)
	}
	return nil
}

// PushItemKey bounds the compound SQL key without truncating a canonical path.
// Adapters also compare the stored path and immutable intent on a collision.
func PushItemKey(action PushAction, path string) (string, error) {
	if (action != PushUpload && action != PushDelete) || !canonicalPushItemPath(path) {
		return "", fmt.Errorf("invalid push operation key: %w", kernel.ErrInvalid)
	}
	digest := sha256.Sum256([]byte(string(action) + "\x00" + path))
	return hex.EncodeToString(digest[:]), nil
}

// PushIntentFingerprint excludes mutable verification/outcome state, allowing
// creation retries to return existing progress for exactly the original intent.
func PushIntentFingerprint(item PushItem) (string, error) {
	intent := struct {
		Run    PushReference              `json:"run"`
		Action PushAction                 `json:"action"`
		Path   string                     `json:"path"`
		Source *artifactsapi.TransferItem `json:"source"`
		Target *PushTargetSnapshot        `json:"target"`
	}{item.Run, item.Action, item.Path, item.Source, item.Target}
	data, err := json.Marshal(kernel.Normalise(intent))
	if err != nil {
		return "", fmt.Errorf("push intent encoding: %w: %w", kernel.ErrInvalid, err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// ValidatePushItemAdvance preserves immutable intent and requires monotonic
// verification before success, including requested metadata synchronization.
func ValidatePushItemAdvance(run PushRun, current, next PushItem) error {
	if err := current.Validate(run); err != nil {
		return err
	}
	if err := next.Validate(run); err != nil {
		return err
	}
	before, err := PushIntentFingerprint(current)
	if err != nil {
		return err
	}
	after, err := PushIntentFingerprint(next)
	if err != nil {
		return err
	}
	if current.Version == math.MaxInt64 || next.Version != current.Version+1 || current.Phase == PushItemComplete ||
		run.Phase.Terminal() || before != after || next.Updated.Before(current.Updated) {
		return fmt.Errorf("push item progress changed: %w", kernel.ErrConflict)
	}
	if next.Phase == PushItemComplete && next.Outcome != PushSucceeded {
		return nil
	}
	switch current.Phase {
	case PushIntent:
		if next.Phase == PushContentVerified || current.Action == PushDelete && next.Phase == PushItemComplete {
			return nil
		}
	case PushContentVerified:
		if next.Phase == PushMetadataVerified || next.Phase == PushItemComplete && !current.Source.PropertiesIncluded && current.Source.Statistics == nil {
			return nil
		}
	case PushMetadataVerified:
		if next.Phase == PushItemComplete {
			return nil
		}
	}
	return fmt.Errorf("push item phase did not advance: %w", kernel.ErrConflict)
}
