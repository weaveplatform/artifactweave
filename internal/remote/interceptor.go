package remote

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/source"
)

// Interceptor reads a remote resource in place of the shared policy.
type Interceptor interface {
	Read(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error)
}

type smartKey struct{}

// WithSmart marks ctx as inside Smart Remote; active enables Smart headers.
func WithSmart(ctx context.Context, active bool) context.Context {
	return context.WithValue(ctx, smartKey{}, active)
}

// SmartActive reports whether ctx was marked active by WithSmart.
func SmartActive(ctx context.Context) bool {
	a, _ := ctx.Value(smartKey{}).(bool)
	return a
}
