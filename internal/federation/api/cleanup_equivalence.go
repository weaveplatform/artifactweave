package api

import (
	"reflect"
	"slices"
)

// SameDownloads compares the immutable repository/member/version identities and
// both public-download and hidden-admission generations of a selected candidate.
// A renewed challenge/window is allowed. Ordinary removal can change a maximum
// recorded timestamp by removing its source file, so timestamps are compared by
// policy separately and cannot substitute for either mutation generation.
func (e CleanupEvidence) SameDownloads(other CleanupEvidence) bool {
	return reflect.DeepEqual(e.Scope, other.Scope) && slices.EqualFunc(e.Pins, other.Pins, func(a, b CleanupPin) bool {
		return a.Member == b.Member && a.Incarnation == b.Incarnation && a.Generation == b.Generation && a.Grant == b.Grant && a.ClockAllowance == b.ClockAllowance &&
			a.CleanupVersion == b.CleanupVersion &&
			a.ID == b.ID &&
			a.Revision == b.Revision &&
			a.AdmissionRevision == b.AdmissionRevision
	})
}
