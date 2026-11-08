package remote_test

import (
	"testing"
	"time"
)

// ptr and waitFor are the in-package helpers of client_test.go and
// flight_test.go, copied for the external tests, which cannot see them.

func ptr[T any](v T) *T { return &v }

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("condition did not become true")
		case <-tick.C:
		}
	}
}
