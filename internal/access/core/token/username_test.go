package token

import (
	"context"
	"testing"
	"time"
)

// TestUnitRefreshAsFoldsUsername: the user a refresh is checked against
// names the token's user in any case (#1432): a transient subject keeps the
// case it was issued with, while the caller authenticated as BOB is bob.
func TestUnitRefreshAsFoldsUsername(t *testing.T) {
	s, _, c := world(t)
	ctx := context.Background()
	bobs := issue(t, s, Request{Username: "Bob", Refreshable: true, ExpiresIn: dur(time.Hour)})
	c.add(2 * time.Hour)
	next, err := s.RefreshAs(ctx, bobs.RefreshToken, RefreshCheck{Username: "bob"})
	if err != nil || next.Record.Subject != bobs.Record.Subject {
		t.Fatalf("RefreshAs as bob = %+v, %v", next, err)
	}
}
