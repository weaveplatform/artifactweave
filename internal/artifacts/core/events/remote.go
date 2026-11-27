package events

import "context"

type remoteKey struct{}

// WithRemote marks trusted ordered federation application. Only internal
// application paths set it; request headers or public context permits do not.
func WithRemote(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteKey{}, true)
}

// IsRemote keeps ordered inbound tombstones applicable behind cleanup barriers.
func IsRemote(ctx context.Context) bool {
	remote, _ := ctx.Value(remoteKey{}).(bool)
	return remote
}
