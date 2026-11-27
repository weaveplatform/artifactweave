package cleanup

import (
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
)

func TestUnitFailureFenceExpiresOnlyBeyondObservationWindow(t *testing.T) {
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fence := failureFence{}
	member := api.CleanupMember{Instance: "member", Grant: "grant"}
	fence.record(member.Instance, member.Grant, base)
	if !fence.changed([]api.CleanupMember{member}, base.Add(Window-time.Nanosecond)) {
		t.Fatal("a pre-failure observation regained authority inside its valid window")
	}
	if fence.current(member.Instance, member.Grant, base.Add(Window+time.Nanosecond)) != 0 {
		t.Fatal("expired process-local failure retained an unused entry")
	}
	// Relationship churn cannot retain all historic grant keys. Every old
	// observation is already expired before its fast failure fence is pruned.
	for n := range 100 {
		fence.record("member", fmt.Sprint(n), base.Add(time.Duration(n+2)*Window))
		if len(fence.epochs) > 2 {
			t.Fatal("historic grants grew the failure cache without bound", len(fence.epochs))
		}
	}
}
