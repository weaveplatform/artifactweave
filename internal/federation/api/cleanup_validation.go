package api

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// CleanupMaximumMembers includes this instance and matches federation's
// supported membership bound. Evidence cannot grow with file traversal.
const CleanupMaximumMembers = 10

func boundedCleanupText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// Validate checks private persisted evidence bounds, not its authenticity.
// Only a fresh signed collection plus current local fences permits removal.
func (e CleanupEvidence) Validate() error {
	if err := e.Scope.validate(); err != nil {
		return err
	}
	s := e.Scope
	if e.Started.IsZero() || !e.Expires.After(e.Started) || len(e.Pins) == 0 || len(e.Pins) > (len(s.Members)+1)*CleanupBatchSize {
		return fmt.Errorf("invalid bounded cleanup evidence: %w", kernel.ErrInvalid)
	}
	members := map[string]bool{s.Self: true}
	for _, member := range s.Members {
		members[member.Instance] = true
	}

	type key struct {
		member  string
		version CleanupVersion
	}
	seen := map[key]bool{}
	local := map[CleanupVersion]bool{}
	counts := map[string]int{}
	for _, pin := range e.Pins {
		if pin.Member == s.Self {
			local[pin.CleanupVersion] = true
		}
	}
	for _, pin := range e.Pins {
		k := key{pin.Member, pin.CleanupVersion}
		if !members[pin.Member] || !local[pin.CleanupVersion] || seen[k] {
			return fmt.Errorf("invalid cleanup version evidence: %w", kernel.ErrInvalid)
		}
		if err := pin.validate(s); err != nil {
			return err
		}
		seen[k] = true
		counts[pin.Member]++
	}
	if len(local) == 0 || len(local) > CleanupBatchSize {
		return kernel.ErrInvalid
	}
	for member := range members {
		if counts[member] != len(local) {
			return fmt.Errorf("incomplete cleanup member evidence: %w", kernel.ErrInvalid)
		}
	}
	return nil
}

func (pin CleanupPin) validate(scope CleanupScope) error {
	if len(pin.AdmissionRevision) > 256 || strings.ContainsRune(pin.AdmissionRevision, 0) || !utf8.ValidString(pin.AdmissionRevision) || pin.ID <= 0 ||
		!boundedCleanupText(pin.Package, 4096) ||
		!boundedCleanupText(pin.Version, 4096) ||
		!boundedCleanupText(pin.Revision, 128) ||
		!boundedCleanupText(pin.Incarnation, 128) ||
		!boundedCleanupText(pin.Generation, 128) ||
		pin.Member != scope.Self && !boundedCleanupText(pin.Grant, 128) {
		return fmt.Errorf("invalid cleanup version evidence: %w", kernel.ErrInvalid)
	}
	if pin.Member == scope.Self && pin.ClockAllowance != 0 || pin.Member != scope.Self && pin.ClockAllowance != CleanupMaximumClockAllowance {
		return fmt.Errorf("invalid cleanup timestamp uncertainty: %w", kernel.ErrInvalid)
	}
	if pin.Member == scope.Self && (pin.Incarnation != scope.Incarnation || pin.Generation != scope.Generation) {
		return fmt.Errorf("cleanup local observation changed scope: %w", kernel.ErrInvalid)
	}
	return nil
}

func (s CleanupScope) validate() error {
	if s.Class != "federated" || !boundedCleanupText(s.PackageType, 64) || len(s.Project) > 1024 || !boundedCleanupText(s.Repository, 1024) ||
		!boundedCleanupText(s.Incarnation, 128) ||
		!boundedCleanupText(s.Generation, 128) ||
		!boundedCleanupText(s.Self, 128) ||
		len(s.Base) > 8192 ||
		len(s.Members) >= CleanupMaximumMembers {
		return fmt.Errorf("invalid bounded cleanup scope: %w", kernel.ErrInvalid)
	}
	members := map[string]bool{s.Self: true}
	for _, m := range s.Members {
		if members[m.Instance] || !boundedCleanupText(m.Instance, 128) || !boundedCleanupText(m.Repository, 1024) || !boundedCleanupText(m.Base, 8192) ||
			!boundedCleanupText(m.Grant, 128) ||
			!boundedCleanupText(m.Failure, 128) ||
			len(m.Mode) > 64 {
			return fmt.Errorf("invalid cleanup member evidence: %w", kernel.ErrInvalid)
		}
		members[m.Instance] = true
	}
	return nil
}
