package api_test

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitCleanupSameDownloads(t *testing.T) {
	base := api.CleanupEvidence{
		Scope: api.CleanupScope{Self: "self", Repository: "repo", Incarnation: "life", Generation: "config"},
		Pins: []api.CleanupPin{
			{
				Member:      "self",
				Incarnation: "life",
				Generation:  "config",
				CleanupDownload: api.CleanupDownload{
					CleanupVersion:    api.CleanupVersion{Package: "widget", Version: "1"},
					ID:                1,
					Revision:          "public-1",
					AdmissionRevision: "private-1",
				},
			},
		},
	}
	if !base.SameDownloads(kernel.Clone(base)) {
		t.Fatal("same immutable download binding differs")
	}
	renewed := kernel.Clone(base)
	renewed.Started = time.Now()
	renewed.Expires = renewed.Started.Add(time.Second)
	renewed.Pins[0].LastDownloaded = time.Now()
	renewed.Pins[0].AdmittedAt = time.Now()
	renewed.Pins[0].Created = time.Now()
	if !base.SameDownloads(renewed) {
		t.Fatal("window renewal/timestamp-source removal manufactured a new download identity")
	}
	for _, tc := range []struct {
		name   string
		change func(*api.CleanupEvidence)
	}{
		{"repository_lifetime", func(e *api.CleanupEvidence) { e.Scope.Incarnation = "replacement" }},
		{"member", func(e *api.CleanupEvidence) { e.Pins[0].Member = "different" }},
		{"member_lifetime", func(e *api.CleanupEvidence) { e.Pins[0].Incarnation = "replacement" }},
		{"member_configuration", func(e *api.CleanupEvidence) { e.Pins[0].Generation = "replacement" }},
		{"trust", func(e *api.CleanupEvidence) { e.Pins[0].Grant = "replacement" }},
		{"clock_boundary", func(e *api.CleanupEvidence) { e.Pins[0].ClockAllowance = time.Second }},
		{"logical_identity", func(e *api.CleanupEvidence) { e.Pins[0].Version = "2" }},
		{"immutable_identity", func(e *api.CleanupEvidence) { e.Pins[0].ID = 2 }},
		{"public_download", func(e *api.CleanupEvidence) { e.Pins[0].Revision = "public-2" }},
		{"hidden_admission", func(e *api.CleanupEvidence) { e.Pins[0].AdmissionRevision = "private-2" }},
		{"missing_member", func(e *api.CleanupEvidence) { e.Pins = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := kernel.Clone(base)
			tc.change(&changed)
			if base.SameDownloads(changed) || changed.SameDownloads(base) {
				t.Fatal("immutable download evidence change was adopted")
			}
		})
	}
}
