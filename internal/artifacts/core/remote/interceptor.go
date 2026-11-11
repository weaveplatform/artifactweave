package remote

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// Interceptor reads a remote resource in place of the shared policy. Once
// installed as Policy.Smart, it receives every Policy.Read whose context
// carries no WithSmart mark, and answers with the source service's errors as
// the policy does. Smart Remote (artifacts/core/remote/smart) is the one
// implementation, and core never imports it (decision 0044): this interface
// and the context mark are all the policy knows of Smart.
type Interceptor interface {
	Read(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error)
}

type smartKey struct{}

// WithSmart marks ctx as inside Smart Remote; active enables Smart headers.
// Policy.Read tests whether the mark is present, not whether it is active:
// Smart's Read marks its context inactive on entry, before it knows whether
// Smart applies, and calls Policy.Read under that mark for the reads it
// leaves to the policy. A test of activity would hand those reads back to
// the interceptor, which would enter itself again without end.
func WithSmart(ctx context.Context, active bool) context.Context {
	return context.WithValue(ctx, smartKey{}, active)
}

// SmartActive reports whether ctx was marked active by WithSmart; an
// inactive mark and no mark both answer false.
func SmartActive(ctx context.Context) bool {
	a, _ := ctx.Value(smartKey{}).(bool)
	return a
}
