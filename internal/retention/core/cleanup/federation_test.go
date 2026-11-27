package cleanup

import (
	"testing"
	"time"

	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitFederatedDownloadClockUncertainty(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	selection := Selection{criteria: newVersionCriteria(model.CleanupCriteria{LastDownloadedBeforeInDays: 1}, now)}
	for _, tc := range []struct {
		name      string
		member    string
		at        time.Time
		allowance time.Duration
		want      Reason
	}{
		{"slow_peer_near_cutoff", "peer", now.Add(-24*time.Hour - 4*time.Minute), federationapi.CleanupMaximumClockAllowance, ReasonDownloaded},
		{"old_beyond_uncertainty", "peer", now.Add(-24*time.Hour - 6*time.Minute), federationapi.CleanupMaximumClockAllowance, ReasonSelected},
		{"local_cutoff_unchanged", "self", now.Add(-24*time.Hour - time.Second), 0, ReasonSelected},
		{"peer_future_download", "peer", now.Add(time.Minute), federationapi.CleanupMaximumClockAllowance, ReasonDownloaded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := federationapi.CleanupEvidence{
				Scope: federationapi.CleanupScope{Self: "self"},
				Pins:  []federationapi.CleanupPin{{Member: tc.member, ClockAllowance: tc.allowance, CleanupDownload: federationapi.CleanupDownload{LastDownloaded: tc.at}}},
			}
			if got := selection.federationDownloadReason(evidence); got != tc.want {
				t.Fatalf("clock-uncertain download reason=%s want=%s", got, tc.want)
			}
		})
	}
}
