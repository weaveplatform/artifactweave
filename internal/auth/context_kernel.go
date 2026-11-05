package auth

import "context"

type ctxKey struct{}

// principalEntry is what WithPrincipal and WithAuthorized store under
// ctxKey. It is stored by pointer because Principal holds slices and cannot
// be compared: a fresh pointer per call is unique and comparable, so it
// identifies the call.
type principalEntry struct{ p Principal }

// WithPrincipal returns ctx carrying p with no permission decider:
// DeciderFrom answers nil for it, as for a principal that was never
// authorised. A principal that Authorize returned goes in with
// WithAuthorized instead, with its decider.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return WithAuthorized(ctx, p, nil)
}

// FromContext returns the request's principal; ok is false when the request
// is unauthenticated (no credentials and anonymous access off).
func FromContext(ctx context.Context) (Principal, bool) {
	e := principalBinding(ctx)
	if e == nil {
		return Principal{}, false
	}
	return e.p, true
}

// principalBinding returns the entry WithPrincipal or WithAuthorized stored
// in ctx, or nil.
func principalBinding(ctx context.Context) *principalEntry {
	e, _ := ctx.Value(ctxKey{}).(*principalEntry)
	return e
}

// RequireAuthenticated returns ErrUnauthenticated unless the request carries
// a user other than anonymous.
func RequireAuthenticated(ctx context.Context) (Principal, error) {
	p, ok := FromContext(ctx)
	if !ok || p.Anonymous {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}

// RequireAdmin returns ErrUnauthenticated without a user and ErrForbidden for
// a user who is not an administrator, the split Artifactory makes between
// 401 and 403.
func RequireAdmin(ctx context.Context) (Principal, error) {
	p, err := RequireAuthenticated(ctx)
	if err != nil {
		return p, err
	}
	if !p.Admin {
		return Principal{}, ErrForbidden
	}
	return p, nil
}
