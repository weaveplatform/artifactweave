package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// CleanupIntentState never changes a terminal outcome back into permission.
type CleanupIntentState string

// CleanupPreparing and the following states describe the durable protocol.
const (
	CleanupPreparing CleanupIntentState = "preparing"
	CleanupPrepared  CleanupIntentState = "prepared"
	CleanupCommitted CleanupIntentState = "committed"
	CleanupAborted   CleanupIntentState = "aborted"
)

// CleanupIntent binds one ordinary indexed-file removal and all of its native
// companions. The outcome commits with those effects and their outbox watermark.
// Participants never release on elapsed time or an uncertain network answer.
type CleanupIntent struct {
	ID              string              `json:"id"`
	Revision        int64               `json:"revision"`
	RunID           int64               `json:"run_id"`
	FileID          int64               `json:"file_id"`
	State           CleanupIntentState  `json:"state"`
	Selected        api.CleanupEvidence `json:"selected"`
	Prepared        api.CleanupEvidence `json:"prepared"`
	Watermark       int64               `json:"watermark"`
	BeforeWatermark int64               `json:"before_watermark"`
	Released        bool                `json:"released"`
	ReleasedMembers []string            `json:"released_members"`
}

// Validate checks storage shape, not current authority to publish an effect.
func (i CleanupIntent) Validate() error {
	if len(i.ID) < 16 || len(i.ID) > 128 || strings.ContainsRune(i.ID, 0) || i.Revision < 1 || i.RunID < 1 || i.FileID < 1 || i.BeforeWatermark < 0 {
		return fmt.Errorf("invalid cleanup operation identity: %w", kernel.ErrInvalid)
	}
	if err := i.Selected.Validate(); err != nil {
		return fmt.Errorf("invalid selected cleanup evidence: %w", err)
	}
	if i.Selected.Scope.Receiver {
		return fmt.Errorf("a receiver cannot coordinate cleanup: %w", kernel.ErrInvalid)
	}
	if err := i.validateReleaseMembers(); err != nil {
		return err
	}
	if err := i.validateOutcome(); err != nil {
		return err
	}

	document, err := json.Marshal(i)
	if err != nil || len(document) > 4<<20 {
		return fmt.Errorf("cleanup operation exceeds its durable bound: %w", kernel.ErrInvalid)
	}
	return nil
}

func (i CleanupIntent) validateReleaseMembers() error {
	known := map[string]bool{i.Selected.Scope.Self: true}
	for _, member := range i.Selected.Scope.Members {
		known[member.Instance] = true
	}
	if !slices.IsSorted(i.ReleasedMembers) || len(i.ReleasedMembers) > len(known) {
		return kernel.ErrInvalid
	}
	for n, member := range i.ReleasedMembers {
		if !known[member] || n > 0 && i.ReleasedMembers[n-1] == member {
			return kernel.ErrInvalid
		}
	}
	if i.Released && len(i.ReleasedMembers) != len(known) {
		return kernel.ErrInvalid
	}
	return nil
}

func (i CleanupIntent) validateOutcome() error {
	switch i.State {
	case CleanupPreparing:
		if !reflect.DeepEqual(i.Prepared, api.CleanupEvidence{}) || i.Watermark != 0 || i.Released || len(i.ReleasedMembers) > 0 {
			return fmt.Errorf("unprepared cleanup operation carries an outcome: %w", kernel.ErrInvalid)
		}
	case CleanupPrepared, CleanupCommitted:
		if i.State == CleanupCommitted && len(i.Selected.Scope.Members) > 0 && i.Watermark <= i.BeforeWatermark {
			return fmt.Errorf("committed cleanup lacks its own tombstone frontier: %w", kernel.ErrInvalid)
		}
		if err := i.Prepared.Validate(); err != nil {
			return fmt.Errorf("invalid prepared cleanup evidence: %w", err)
		}
		if !reflect.DeepEqual(i.Selected.Scope, i.Prepared.Scope) || i.Watermark < 0 ||
			i.State == CleanupPrepared && (i.Watermark != 0 || i.Released || len(i.ReleasedMembers) > 0) {
			return fmt.Errorf("prepared cleanup operation changed its binding: %w", kernel.ErrInvalid)
		}
	case CleanupAborted:
		if !reflect.DeepEqual(i.Prepared, api.CleanupEvidence{}) {
			if err := i.Prepared.Validate(); err != nil {
				return fmt.Errorf("invalid aborted cleanup evidence: %w", err)
			}
			if !reflect.DeepEqual(i.Prepared.Scope, i.Selected.Scope) {
				return kernel.ErrInvalid
			}
		}
		if i.Watermark != 0 {
			return fmt.Errorf("aborted cleanup operation carries committed effects: %w", kernel.ErrInvalid)
		}
	default:
		return fmt.Errorf("invalid cleanup operation state: %w", kernel.ErrInvalid)
	}
	return nil
}

// ValidateCleanupIntentAdvance fences both stale workers and outcome reversal.
func ValidateCleanupIntentAdvance(old, next CleanupIntent) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if old.ID != next.ID || old.RunID != next.RunID || old.FileID != next.FileID || old.Revision < 1 || next.Revision != old.Revision+1 ||
		!reflect.DeepEqual(old.Selected, next.Selected) || old.BeforeWatermark != next.BeforeWatermark {
		return fmt.Errorf("cleanup operation binding changed: %w", kernel.ErrConflict)
	}
	switch old.State {
	case CleanupPreparing:
		if next.State != CleanupPrepared && next.State != CleanupAborted {
			return fmt.Errorf("cleanup operation was not prepared: %w", kernel.ErrConflict)
		}
	case CleanupPrepared:
		if next.State != CleanupCommitted && next.State != CleanupAborted || !reflect.DeepEqual(old.Prepared, next.Prepared) {
			return fmt.Errorf("cleanup operation cannot publish this outcome: %w", kernel.ErrConflict)
		}
	case CleanupCommitted, CleanupAborted:
		if next.State != old.State || next.Watermark != old.Watermark || old.Released || !reflect.DeepEqual(old.Prepared, next.Prepared) ||
			len(next.ReleasedMembers) < len(old.ReleasedMembers) {
			return fmt.Errorf("cleanup operation outcome is irreversible: %w", kernel.ErrConflict)
		}
		for _, member := range old.ReleasedMembers {
			if !slices.Contains(next.ReleasedMembers, member) {
				return fmt.Errorf("cleanup release acknowledgement cannot be lost: %w", kernel.ErrConflict)
			}
		}
		if len(next.ReleasedMembers) == len(old.ReleasedMembers) && next.Released == old.Released {
			return fmt.Errorf("cleanup settlement made no progress: %w", kernel.ErrConflict)
		}

	default:
		return fmt.Errorf("invalid previous cleanup operation: %w", kernel.ErrConflict)
	}
	return nil
}
