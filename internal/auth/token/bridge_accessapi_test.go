package token

import (
	"context"
	"errors"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	coretoken "github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// The token API and npm logout answer a short-lived token's revocation by
// testing errors.Is against ErrNotRevocable. A copied sentinel would print
// the same text but stop matching once a caller tests the access/api name,
// so the test checks that both names are one value, also on the error that
// the service returns. The service lives in access/core/token, so the test
// builds one through that package's exported API, with a small key.
func TestUnitBridgeAccessAPINotRevocableSentinel(t *testing.T) {
	legacy, target := ErrNotRevocable, accessapi.ErrNotRevocable
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("token.ErrNotRevocable and accessapi.ErrNotRevocable are different errors")
	}
	ctx := context.Background()
	db := inmem.New()
	if err := db.Instance().Init(ctx, instance.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	s, err := coretoken.New(ctx, db, coretoken.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Hour
	short, err := s.Issue(ctx, coretoken.Request{Username: "alice", ExpiresIn: &hour})
	if err != nil {
		t.Fatal(err)
	}
	err = s.RevokeValue(ctx, short.AccessToken)
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("RevokeValue error %v does not match ErrNotRevocable under both names", err)
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "token: token is not revocable" {
			t.Errorf("ErrNotRevocable.Error() = %q, want %q", got, "token: token is not revocable")
		}
	}
}
