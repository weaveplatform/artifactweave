package peer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
)

// TestUnitPairCallErrors sorts what a failed pairing call reports: a call
// that got no answer is unreachable, an unsigned refusal of a redeem keeps
// its status, any other unsigned answer is unverified, and this instance's
// own configuration or bounds are neither.
func TestUnitPairCallErrors(t *testing.T) {
	unsigned := func(status int) error { return &UnverifiedAnswerError{Status: status, Err: ErrNoPeerKey} }
	var status *pairing.StatusError
	if err := callError(unsigned(403), true); !errors.As(err, &status) || status.Status != 403 {
		t.Errorf("unsigned redeem refusal: %v", err)
	}
	if err := callError(unsigned(200), true); !errors.Is(err, pairing.ErrUnverified) {
		t.Errorf("unsigned redeem answer: %v", err)
	}
	if err := callError(unsigned(401), false); !errors.Is(err, pairing.ErrUnverified) {
		t.Errorf("unsigned cancel answer: %v", err)
	}
	for _, own := range []error{outbound.ErrConfiguration, ErrTooLarge} {
		err := callError(fmt.Errorf("x: %w", own), true)
		if errors.Is(err, pairing.ErrUnreachable) || errors.Is(err, pairing.ErrUnverified) || !errors.Is(err, own) {
			t.Errorf("%v: %v", own, err)
		}
	}
	if err := callError(outbound.ErrConnection, true); !errors.Is(err, pairing.ErrUnreachable) {
		t.Errorf("connection: %v", err)
	}
}
