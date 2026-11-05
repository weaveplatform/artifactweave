package token

import (
	"context"
	"errors"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
)

// The token API and npm logout answer a short-lived token's revocation by
// testing errors.Is against ErrNotRevocable. A copied sentinel would print
// the same text but stop matching once a caller tests the access/api name,
// so the test checks that both names are one value, also on the error that
// the service returns.
func TestUnitBridgeAccessAPINotRevocableSentinel(t *testing.T) {
	legacy, target := ErrNotRevocable, accessapi.ErrNotRevocable
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("token.ErrNotRevocable and accessapi.ErrNotRevocable are different errors")
	}
	s, _, _ := world(t)
	short := issue(t, s, Request{ExpiresIn: dur(time.Hour)})
	err := s.RevokeValue(context.Background(), short.AccessToken)
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("RevokeValue error %v does not match ErrNotRevocable under both names", err)
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "token: token is not revocable" {
			t.Errorf("ErrNotRevocable.Error() = %q, want %q", got, "token: token is not revocable")
		}
	}
}
