package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PullReference identifies one private logical transfer lifetime, independently
// of a dispatch attempt, queue generation or public completion timestamp.
type PullReference struct {
	ID          string `json:"id"`
	Incarnation string `json:"incarnation"`
}

// PullBinding pins owning source/layout/options and original caller references.
// Source.Revision is opaque; no source URL, password or authorization is stored.
type PullBinding struct {
	TargetKey      string                      `json:"target_key"`
	TargetRevision string                      `json:"target_revision"`
	Source         artifactsapi.PullRepository `json:"source"`
	Credential     kernel.CredentialReference  `json:"credential"`
	Root           string                      `json:"root"`
	Include        string                      `json:"include"`
	Exclude        string                      `json:"exclude"`
	SyncDeletes    bool                        `json:"sync_deletes"`
	SyncProperties bool                        `json:"sync_properties"`
}

// Selection returns the immutable canonical scope sent to the artifact owner.
func (b PullBinding) Selection() artifactsapi.PullSelection {
	return artifactsapi.PullSelection{Root: b.Root, Include: b.Include, Exclude: b.Exclude, Properties: b.SyncProperties}
}

// Validate bounds logical identity without granting current execution rights.
func (r PullReference) Validate() error {
	if !nonemptyText(r.ID) || !nonemptyText(r.Incarnation) || len(r.ID) > 256 || len(r.Incarnation) > 256 {
		return fmt.Errorf("invalid pull reference: %w", kernel.ErrInvalid)
	}
	return nil
}

// Validate requires a complete source/caller binding and canonical selection.
func (b PullBinding) Validate() error {
	for _, text := range []string{
		b.TargetKey, b.TargetRevision, b.Source.Repository.Key, b.Source.Repository.Incarnation,
		b.Source.Repository.Generation, string(b.Source.Repository.PackageType), b.Credential.Name,
	} {
		if !nonemptyText(text) {
			return fmt.Errorf("incomplete pull binding: %w", kernel.ErrInvalid)
		}
	}
	if !pushDigest(b.Source.Revision) || b.Credential.Anonymous || b.Credential.Registry != nil ||
		!privateText(b.Credential.TokenID) || !b.Credential.Password && b.Credential.TokenID == "" ||
		!privateText(b.Include) || !privateText(b.Exclude) || b.Root != "" && !canonicalPushItemPath(b.Root) {
		return fmt.Errorf("invalid pull provenance: %w", kernel.ErrInvalid)
	}
	return nil
}

// PullPhase separates peer enumeration from local-cache reconciliation.
type PullPhase string

// Private durable traversal phases, independent of public replication status.
const (
	PullCopy      PullPhase = "copy"
	PullReconcile PullPhase = "reconcile"
	PullComplete  PullPhase = "complete"
	PullFailed    PullPhase = "failed"
)

// Terminal refuses further network work for a settled logical transfer.
func (p PullPhase) Terminal() bool { return p == PullComplete || p == PullFailed }

// PullRun retains bounded source/cache positions across queue retries/restart.
// TargetCursor/TargetDone describe local cache reconciliation, never a peer write.
type PullRun struct {
	Reference    PullReference `json:"reference"`
	Binding      PullBinding   `json:"binding"`
	Version      int64         `json:"version"`
	Phase        PullPhase     `json:"phase"`
	SourceCursor string        `json:"source_cursor"`
	TargetCursor string        `json:"target_cursor"`
	SourceDone   bool          `json:"source_done"`
	TargetDone   bool          `json:"target_done"`
	Started      time.Time     `json:"started"`
	Updated      time.Time     `json:"updated"`
	Completed    time.Time     `json:"completed"`
}

// NewPullRun creates an unexecuted lifetime; stores preserve it on binding retries.
func NewPullRun(id string, binding PullBinding, at time.Time) (PullRun, error) {
	at = at.UTC().Truncate(time.Microsecond)
	run := PullRun{
		Reference: PullReference{ID: id, Incarnation: rand.Text()}, Binding: binding,
		Version: 1, Phase: PullCopy, Started: at, Updated: at,
	}
	return run, run.Validate()
}

// Validate checks traversal and lifecycle clocks without consulting permissions.
func (r PullRun) Validate() error {
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if r.Version < 1 || r.Started.IsZero() || r.Updated.Before(r.Started) || !privateText(r.SourceCursor) || !privateText(r.TargetCursor) ||
		r.SourceDone && r.SourceCursor != "" || r.TargetDone && r.TargetCursor != "" ||
		r.Phase.Terminal() != !r.Completed.IsZero() || !r.Completed.IsZero() &&
		(r.Completed.Before(r.Started) || r.Updated.Before(r.Completed)) {
		return fmt.Errorf("invalid pull traversal: %w", kernel.ErrInvalid)
	}
	switch r.Phase {
	case PullCopy:
		if r.TargetDone || r.TargetCursor != "" {
			return fmt.Errorf("pull reconciliation preceded copying: %w", kernel.ErrInvalid)
		}
	case PullReconcile:
		if !r.SourceDone || !r.Binding.SyncDeletes {
			return fmt.Errorf("pull reconciliation has no completed source: %w", kernel.ErrInvalid)
		}
	case PullComplete:
		if !r.SourceDone || r.Binding.SyncDeletes && !r.TargetDone {
			return fmt.Errorf("pull completed unfinished traversal: %w", kernel.ErrInvalid)
		}
	case PullFailed:
	default:
		return fmt.Errorf("invalid pull phase: %w", kernel.ErrInvalid)
	}
	return nil
}

// ValidatePullAdvance preserves immutable binding and monotonically closed cursors.
func ValidatePullAdvance(current, next PullRun) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if current.Validate() != nil || current.Phase.Terminal() || current.Version == math.MaxInt64 || next.Version != current.Version+1 ||
		current.Reference != next.Reference || current.Binding != next.Binding || !current.Started.Equal(next.Started) || next.Updated.Before(current.Updated) ||
		current.SourceDone && !next.SourceDone || current.TargetDone && !next.TargetDone ||
		current.Phase == PullReconcile && next.Phase == PullCopy {
		return fmt.Errorf("stale pull traversal: %w", kernel.ErrConflict)
	}
	return nil
}

// PullAction identifies one immutable selected operation.
type PullAction string

type (
	// PullItemPhase distinguishes pending intent from actual evidence.
	PullItemPhase string
	// PullOutcome records a verified effect or a sanitized terminal refusal.
	PullOutcome string
)

// Operations, intent phases and sanitized terminal outcomes.
const (
	PullFetch        PullAction    = "fetch"
	PullDelete       PullAction    = "delete"
	PullIntent       PullItemPhase = "intent"
	PullItemComplete PullItemPhase = "complete"
	PullSucceeded    PullOutcome   = "succeeded"
	PullSkipped      PullOutcome   = "skipped"
	PullItemFailed   PullOutcome   = "failed"
)

// PullItem records selected facts before I/O and actual owner receipts after it.
// Failure reasons are closed local codes, never remote bodies/URLs/credentials.
type PullItem struct {
	Run     PullReference                    `json:"run"`
	Action  PullAction                       `json:"action"`
	Path    string                           `json:"path"`
	Source  *artifactsapi.PullItem           `json:"source"`
	Target  *artifactsapi.PullCacheCandidate `json:"target"`
	Cache   *artifactsapi.PullCached         `json:"cache"`
	Removed []string                         `json:"removed"`
	Version int64                            `json:"version"`
	Phase   PullItemPhase                    `json:"phase"`
	Outcome PullOutcome                      `json:"outcome"`
	Reason  string                           `json:"reason"`
	Updated time.Time                        `json:"updated"`
}

// Validate checks selected and committed facts against the immutable run scope.
func (i PullItem) Validate(run PullRun) error {
	if err := i.Run.Validate(); err != nil {
		return err
	}
	if i.Run != run.Reference || i.Version < 1 || !canonicalPushItemPath(i.Path) || i.Updated.IsZero() ||
		run.Binding.Root != "" && i.Path != run.Binding.Root && !strings.HasPrefix(i.Path, run.Binding.Root+"/") {
		return fmt.Errorf("invalid pull intent identity: %w", kernel.ErrInvalid)
	}
	switch i.Action {
	case PullFetch:
		if i.Source == nil || i.Target != nil || i.Source.Path != i.Path || i.Source.Repository != run.Binding.Source ||
			i.Source.Selection != run.Binding.Selection() || i.Source.Size < 0 || !pushDigest(i.Source.Actual.SHA256) ||
			!pushDigest(i.Source.Version) || !canonicalPushItemPath(i.Source.UpstreamPath) {
			return fmt.Errorf("invalid pull source intent: %w", kernel.ErrInvalid)
		}
	case PullDelete:
		if i.Source != nil || i.Target == nil || !run.Binding.SyncDeletes || i.Target.Path != i.Path ||
			i.Target.Repository != run.Binding.Source || i.Target.Selection != run.Binding.Selection() || !pushDigest(i.Target.Version) {
			return fmt.Errorf("invalid pull cache deletion intent: %w", kernel.ErrInvalid)
		}
	default:
		return fmt.Errorf("invalid pull action: %w", kernel.ErrInvalid)
	}
	return i.evidence(run.Binding.Root)
}

func (i PullItem) evidence(root string) error {
	if i.Phase == PullIntent {
		if i.Outcome == "" && i.Reason == "" && i.Cache == nil && len(i.Removed) == 0 {
			return nil
		}
		return fmt.Errorf("unexecuted pull carries terminal evidence: %w", kernel.ErrInvalid)
	}
	if i.Phase != PullItemComplete {
		return fmt.Errorf("invalid pull intent phase: %w", kernel.ErrInvalid)
	}
	if i.Outcome == PullSkipped || i.Outcome == PullItemFailed {
		if pushReason(i.Reason) && i.Cache == nil && len(i.Removed) == 0 {
			return nil
		}
		return fmt.Errorf("pull failure has unsafe or successful evidence: %w", kernel.ErrInvalid)
	}
	if i.Outcome != PullSucceeded || i.Reason != "" {
		return fmt.Errorf("invalid pull terminal outcome: %w", kernel.ErrInvalid)
	}
	if i.Action == PullFetch {
		if i.Cache == nil || i.Source == nil || !reflect.DeepEqual(i.Cache.Item, *i.Source) ||
			!pushDigest(i.Cache.Version) || !pushDigest(i.Cache.Representation) || len(i.Removed) != 0 {
			return fmt.Errorf("pull success has no matching persisted cache receipt: %w", kernel.ErrInvalid)
		}
		return nil
	}
	return i.removalProof(root)
}

func (i PullItem) removalProof(root string) error {
	if i.Cache != nil || len(i.Removed) == 0 || len(i.Removed) > 65 || !slices.Contains(i.Removed, i.Path) {
		return fmt.Errorf("pull deletion has no selected removal proof: %w", kernel.ErrInvalid)
	}
	seen := map[string]bool{}
	for _, path := range i.Removed {
		if !canonicalPushItemPath(path) || seen[path] || root != "" && path != root && !strings.HasPrefix(path, root+"/") {
			return fmt.Errorf("invalid pull removal proof: %w", kernel.ErrInvalid)
		}
		seen[path] = true
	}
	return nil
}

// PullItemKey bounds database keys without truncating canonical selected paths.
func PullItemKey(action PullAction, path string) (string, error) {
	if action != PullFetch && action != PullDelete || !canonicalPushItemPath(path) {
		return "", kernel.ErrInvalid
	}
	digest := sha256.Sum256([]byte(string(action) + "\x00" + path))
	return hex.EncodeToString(digest[:]), nil
}

// PullIntentFingerprint excludes mutable receipts, removal results and outcomes.
func PullIntentFingerprint(i PullItem) (string, error) {
	raw, err := json.Marshal(kernel.Normalise(struct {
		Run    PullReference                    `json:"run"`
		Action PullAction                       `json:"action"`
		Path   string                           `json:"path"`
		Source *artifactsapi.PullItem           `json:"source"`
		Target *artifactsapi.PullCacheCandidate `json:"target"`
	}{i.Run, i.Action, i.Path, i.Source, i.Target}))
	if err != nil {
		return "", fmt.Errorf("encode pull intent: %w: %w", kernel.ErrInvalid, err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// ValidatePullItemCreation admits only a fresh intent in its traversal phase.
func ValidatePullItemCreation(run PullRun, i PullItem) error {
	if err := i.Validate(run); err != nil {
		return err
	}
	if run.Phase.Terminal() || i.Version != 1 || i.Phase != PullIntent ||
		i.Action == PullFetch && (run.Phase != PullCopy || run.SourceDone) ||
		i.Action == PullDelete && (run.Phase != PullReconcile || run.TargetDone) {
		return fmt.Errorf("pull traversal cannot admit this intent: %w", kernel.ErrConflict)
	}
	return nil
}

// ValidatePullItemAdvance requires one atomic verified owner result or safe refusal.
func ValidatePullItemAdvance(run PullRun, current, next PullItem) error {
	if err := current.Validate(run); err != nil {
		return err
	}
	if err := next.Validate(run); err != nil {
		return err
	}
	before, err := PullIntentFingerprint(current)
	if err != nil {
		return err
	}
	after, err := PullIntentFingerprint(next)
	if err != nil {
		return err
	}
	if run.Phase.Terminal() || current.Version == math.MaxInt64 || next.Version != current.Version+1 ||
		current.Phase != PullIntent || next.Phase != PullItemComplete || before != after || next.Updated.Before(current.Updated) {
		return fmt.Errorf("stale or rebound pull intent: %w", kernel.ErrConflict)
	}
	return nil
}
