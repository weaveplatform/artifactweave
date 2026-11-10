package work_test

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func TestUnitLeaseHeld(t *testing.T) {
	l := work.Lease{Expires: t0}
	if !l.Held(t0.Add(-time.Second)) || l.Held(t0) {
		t.Error("Lease.Held")
	}
}
