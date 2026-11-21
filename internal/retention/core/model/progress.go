package model

import (
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// CleanupPhase is private durable traversal state, not a public run status.
type CleanupPhase string

// Cleanup phases track bounded traversal separately from public run status.
const (
	CleanupRepositories CleanupPhase = "repositories"
	CleanupPackages     CleanupPhase = "packages"
	CleanupVersions     CleanupPhase = "versions"
	CleanupCandidates   CleanupPhase = "candidates"
	CleanupRemoving     CleanupPhase = "removing"
	CleanupSkipping     CleanupPhase = "skipping"
	CleanupFinished     CleanupPhase = "finished"
)

// CleanupProgress contains bounded traversal references and continuations.
// Parent continuations are saved before child removal can delete an index row.
// Revision fences a stale worker independently of the queue's lease generation.
type CleanupProgress struct {
	RunID, Revision                                        int64 `json:"-"`
	Phase                                                  CleanupPhase
	Repository                                             artifactsapi.IndexRepository
	Package                                                artifactsapi.IndexPackage
	Version                                                artifactsapi.IndexVersion
	RepositoryAfter, PackageAfter, VersionAfter, FileAfter string
	RepositoryDone, PackageDone, VersionDone               bool
	Reason                                                 string
	HadErrors                                              bool
}

// ValidateCleanupProgressAdvance rejects a stale continuation or terminal
// resurrection. The owning executor supplies the current lease fence too.
func ValidateCleanupProgressAdvance(old, next CleanupProgress) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if old.RunID != next.RunID || old.Revision < 1 || next.Revision != old.Revision+1 ||
		old.Phase == CleanupFinished || old.HadErrors && !next.HadErrors {
		return fmt.Errorf("stale cleanup progress: %w", kernel.ErrConflict)
	}
	return nil
}

// Validate checks persisted identity, references and bounded continuations.
func (p CleanupProgress) Validate() error {
	if p.RunID <= 0 || p.Revision <= 0 {
		return fmt.Errorf("invalid cleanup progress identity: %w", kernel.ErrInvalid)
	}
	for _, value := range []string{p.RepositoryAfter, p.PackageAfter, p.VersionAfter, p.FileAfter, p.Reason} {
		if len(value) > 8192 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid cleanup progress continuation: %w", kernel.ErrInvalid)
		}
	}
	switch p.Phase {
	case CleanupRepositories, CleanupFinished:
	case CleanupPackages:
		if p.Repository.Key == "" {
			return fmt.Errorf("cleanup progress has no repository: %w", kernel.ErrInvalid)
		}
	case CleanupVersions:
		if p.Package.ID <= 0 {
			return fmt.Errorf("cleanup progress has no package: %w", kernel.ErrInvalid)
		}
	case CleanupCandidates, CleanupRemoving, CleanupSkipping:
		if p.Version.ID <= 0 {
			return fmt.Errorf("cleanup progress has no version: %w", kernel.ErrInvalid)
		}
	default:
		return fmt.Errorf("invalid cleanup progress phase: %w", kernel.ErrInvalid)
	}
	return nil
}

type (
	// CleanupEntryKind distinguishes indexed file intents from version decisions.
	CleanupEntryKind string
	// CleanupEntryState records selection, refusal or an acknowledged effect.
	CleanupEntryState string
)

// Cleanup entry kinds and states are private reporting and recovery facts.
const (
	CleanupItemEntry    CleanupEntryKind  = "item"
	CleanupVersionEntry CleanupEntryKind  = "version"
	CleanupPending      CleanupEntryState = "pending"
	CleanupSelected     CleanupEntryState = "selected"
	CleanupDeleted      CleanupEntryState = "deleted"
	CleanupSkipped      CleanupEntryState = "skipped"
	CleanupEntryFailed  CleanupEntryState = "failed"
)

// CleanupEntry keeps one selected item or one explained version decision.
// Snapshot is immutable. Removed records the normal item service's actual
// effect, including a companion not previously enumerated as a candidate.
// Revision and a unique (run,kind,entity) key prevent duplicate acknowledgements.
type CleanupEntry struct {
	RunID, EntityID, VersionID, Revision int64            `json:"-"`
	Kind                                 CleanupEntryKind `json:"-"`
	State                                CleanupEntryState
	Snapshot                             artifactsapi.IndexFile
	Removed                              *artifactsapi.RemovedItem
	Reason                               string
}

// Validate checks that an outcome has an indexed intent or an actual effect.
func (e CleanupEntry) Validate() error {
	if e.RunID <= 0 || e.EntityID <= 0 || e.VersionID <= 0 || e.Revision <= 0 ||
		len(e.Reason) > 256 || !utf8.ValidString(e.Reason) || strings.ContainsRune(e.Reason, 0) {
		return fmt.Errorf("invalid cleanup entry identity: %w", kernel.ErrInvalid)
	}
	if e.Kind != CleanupItemEntry && e.Kind != CleanupVersionEntry {
		return fmt.Errorf("invalid cleanup entry kind: %w", kernel.ErrInvalid)
	}
	switch e.State {
	case CleanupPending, CleanupSelected, CleanupSkipped, CleanupEntryFailed:
		if e.Removed != nil {
			return fmt.Errorf("uncommitted cleanup entry has an effect: %w", kernel.ErrInvalid)
		}
	case CleanupDeleted:
		if e.Kind != CleanupItemEntry || e.Removed == nil || e.Removed.ID != e.EntityID || e.Removed.Size < 0 {
			return fmt.Errorf("cleanup acknowledgement has no matching effect: %w", kernel.ErrInvalid)
		}
	default:
		return fmt.Errorf("invalid cleanup entry state: %w", kernel.ErrInvalid)
	}
	if e.Kind == CleanupItemEntry && (e.State == CleanupPending || e.State == CleanupSelected) && (e.Snapshot.ID != e.EntityID || e.Snapshot.Version.ID != e.VersionID) {
		return fmt.Errorf("cleanup intent has no indexed snapshot: %w", kernel.ErrInvalid)
	}
	return nil
}

// SameCleanupIntent ignores mutable outcome/revision while retaining every
// original indexed fact. Re-enumeration may find an already acknowledged row.
func SameCleanupIntent(a, b CleanupEntry) bool {
	return a.RunID == b.RunID && a.Kind == b.Kind && a.EntityID == b.EntityID && a.VersionID == b.VersionID &&
		reflect.DeepEqual(a.Snapshot, b.Snapshot)
}

// ValidateCleanupEntryAdvance fences stale acknowledgements and intent changes.
func ValidateCleanupEntryAdvance(old, next CleanupEntry) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if !SameCleanupIntent(old, next) || next.Revision != old.Revision+1 || old.State == CleanupDeleted ||
		old.State != CleanupPending && next.State != CleanupDeleted && old.Kind == CleanupItemEntry && (old.State != CleanupSelected || next.State != CleanupSkipped) {
		return fmt.Errorf("stale cleanup entry: %w", kernel.ErrConflict)
	}
	return nil
}
