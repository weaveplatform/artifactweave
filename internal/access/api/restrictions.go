package api

import (
	"context"
	"time"
)

// Restrictions checks the temporary restriction an administrator can put on
// a user's authenticated API calls. A module that verifies a credential
// outside the request's authentication, such as a refresh grant or a signed
// download, checks it before it acts for the user. Each call uses ctx for the
// store it reads, so a call inside a store transaction takes part in it.
type Restrictions interface {
	// Check returns nil when user may call the API at now: no restriction is
	// stored for them, it has ended, or ctx is marked for the administrator
	// recovery routes. While a restriction lasts it returns an error that
	// answers 403 "API access is temporarily restricted". It reads the
	// stored restriction on every call, with no cache, and fails closed: a
	// restriction it cannot read is an error.
	Check(ctx context.Context, user string, now time.Time) error
}
