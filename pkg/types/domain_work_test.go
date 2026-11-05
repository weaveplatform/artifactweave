package types_test

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitLeaseHeld(t *testing.T) {
	l := types.Lease{Expires: t0}
	if !l.Held(t0.Add(-time.Second)) || l.Held(t0) {
		t.Error("Lease.Held")
	}
}
