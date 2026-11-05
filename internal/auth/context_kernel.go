package auth

import "context"

type ctxKey struct{}

// principalEntry is what WithPrincipal stores under ctxKey. It is stored by
// pointer because Principal holds slices and cannot be compared: a fresh
// pointer per call is unique and comparable, so it identifies the call.
type principalEntry struct{ p Principal }

// WithPrincipal returns ctx carrying p. Each call stores a new binding (see
// PrincipalBinding), so a permission decider that access bound to an
// earlier call never answers for p; access stores a principal it has
// authorised together with that principal's decider instead.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, &principalEntry{p: p})
}

// FromContext returns the request's principal; ok is false when the request
// is unauthenticated (no credentials and anonymous access off).
func FromContext(ctx context.Context) (Principal, bool) {
	e, _ := PrincipalBinding(ctx).(*principalEntry)
	if e == nil {
		return Principal{}, false
	}
	return e.p, true
}

// PrincipalBinding identifies the WithPrincipal call that set ctx's principal
// (nil when none). Access binds the request's evaluator to it. Bindings
// compare with ==: two calls never share one, and a context derived from ctx
// other than by WithPrincipal keeps ctx's.
func PrincipalBinding(ctx context.Context) any { return ctx.Value(ctxKey{}) }

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
