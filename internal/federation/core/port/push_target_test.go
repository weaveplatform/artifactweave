package port_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
)

func TestUnitPushTargetFailuresKeepOnlyClosedReasonCodes(t *testing.T) {
	for failure, want := range map[port.PushTargetFailure]string{
		port.PushTargetUnauthorized: "target_unauthorized",
		port.PushTargetForbidden:    "target_forbidden",
		port.PushTargetChecksum:     "target_checksum_mismatch",
		port.PushTargetMetadata:     "target_metadata_mismatch",
		port.PushTargetInvalid:      "target_invalid_response",
		"secret remote body":        "target_invalid_response",
	} {
		if failure.Reason() != want || failure.Error() != "replication: "+want || strings.Contains(failure.Error(), "secret") {
			t.Fatal("unbounded remote detail escaped failure classification")
		}
	}
}
