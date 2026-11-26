package peers_test

import (
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/test/peers"
)

// Temporary diagnosis probe; not a proposed production or acceptance change.
func TestAccAdmissionDeadlineProbe(t *testing.T) {
	for n := range 8 {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			testFederatedFormats(t, peers.SQLite)
		})
	}
}
