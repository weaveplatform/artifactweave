package api_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitCleanupEvidenceBounds(t *testing.T) {
	scope := api.CleanupScope{
		Repository: "packages",
		Class:      "federated", PackageType: "npm",
		Incarnation: "local-life",
		Generation:  "local-generation",
		Self:        "self",
		Base:        "https://self.example",
		Members: []api.CleanupMember{
			{Instance: "member", Base: "https://member.example", Repository: "packages", Grant: "grant", Failure: "initial"},
		},
	}
	now := time.Now()
	local := api.CleanupPin{
		Member:          "self",
		Incarnation:     scope.Incarnation,
		Generation:      scope.Generation,
		CleanupDownload: api.CleanupDownload{CleanupVersion: api.CleanupVersion{Package: "name", Version: "1"}, ID: 1, Revision: "downloads"},
	}
	member := local
	member.Member = "member"
	member.Incarnation = "remote-life"
	member.Generation = "remote-generation"
	member.Grant = "remote-grant"
	member.ClockAllowance = api.CleanupMaximumClockAllowance
	valid := api.CleanupEvidence{Scope: scope, Started: now, Expires: now.Add(5 * time.Second), Pins: []api.CleanupPin{local, member}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*api.CleanupEvidence)
	}{
		{"local_clock_allowance", func(e *api.CleanupEvidence) { e.Pins[0].ClockAllowance = api.CleanupMaximumClockAllowance }},
		{"remote_clock_allowance", func(e *api.CleanupEvidence) { e.Pins[1].ClockAllowance = 0 }},
		{"missing_scope", func(e *api.CleanupEvidence) { e.Scope.Repository = "" }},
		{"invalid_UTF8", func(e *api.CleanupEvidence) { e.Scope.Self = "\xff" }},
		{"embedded_NUL", func(e *api.CleanupEvidence) { e.Scope.Self = "self\x00" }},
		{"oversized_base", func(e *api.CleanupEvidence) { e.Scope.Base = strings.Repeat("x", 8193) }},
		{"too_many_members", func(e *api.CleanupEvidence) { e.Scope.Members = make([]api.CleanupMember, api.CleanupMaximumMembers) }},
		{"no_window", func(e *api.CleanupEvidence) { e.Started = time.Time{} }},
		{"reversed_window", func(e *api.CleanupEvidence) { e.Expires = e.Started }},
		{"empty_pins", func(e *api.CleanupEvidence) { e.Pins = nil }},
		{"oversized_pins", func(e *api.CleanupEvidence) { e.Pins = make([]api.CleanupPin, 129) }},
		{"duplicate_member", func(e *api.CleanupEvidence) { e.Scope.Members[0].Instance = e.Scope.Self }},
		{"missing_grant", func(e *api.CleanupEvidence) { e.Scope.Members[0].Grant = "" }},
		{"foreign_pin", func(e *api.CleanupEvidence) { e.Pins[1].Member = "foreign" }},
		{"foreign_version", func(e *api.CleanupEvidence) { e.Pins[1].Version = "foreign" }},
		{"duplicate_pin", func(e *api.CleanupEvidence) { e.Pins = append(e.Pins, e.Pins[1]) }},
		{"invalid_version_ID", func(e *api.CleanupEvidence) { e.Pins[1].ID = 0 }},
		{"invalid_remote_grant", func(e *api.CleanupEvidence) { e.Pins[1].Grant = "" }},
		{"local_incarnation_changed", func(e *api.CleanupEvidence) { e.Pins[0].Incarnation = "other" }},
		{"local_generation_changed", func(e *api.CleanupEvidence) { e.Pins[0].Generation = "other" }},
		{"missing_local", func(e *api.CleanupEvidence) { e.Pins = e.Pins[1:] }},
		{"incomplete_members", func(e *api.CleanupEvidence) { e.Pins = e.Pins[:1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := kernel.Clone(valid)
			tc.change(&evidence)
			if err := evidence.Validate(); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("corrupt private evidence was accepted", err)
			}
		})
	}
}
