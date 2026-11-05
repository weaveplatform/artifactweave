package accessapitest

import (
	"context"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
)

var _ accessapi.Restrictions = (*Restrictions)(nil)

// Restrictions is a Restrictions whose CheckFunc scripts its answer: a test
// restricts a user by returning kernel.ErrAPIRestricted for them, which
// answers 403 "API access is temporarily restricted" as the access module's
// check does, and fails the check by returning any other error. It holds no
// state, so it may be used from several goroutines as long as CheckFunc may.
type Restrictions struct {
	// CheckFunc answers Check. Nil restricts no one.
	CheckFunc func(ctx context.Context, user string, now time.Time) error
}

// Check answers with CheckFunc, or nil when it is nil.
func (r *Restrictions) Check(ctx context.Context, user string, now time.Time) error {
	if r.CheckFunc == nil {
		return nil
	}
	return r.CheckFunc(ctx, user, now)
}
