package api

import (
	"context"
	"reflect"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

type deciderKey struct{}

// boundDecider is what WithDecider stores: the decider and the binding of the
// principal it answers for. Keeping the decider beside the principal, rather
// than in it, leaves kernel.Principal a plain value that carries no evaluator
// (decision 0006).
type boundDecider struct {
	binding any
	d       Decider
}

// WithCaller returns ctx carrying p, stored with kernel.WithPrincipal, and d
// as p's decider (WithDecider). It is how a principal that Authorizer
// evaluated enters a context: a principal stored with kernel.WithPrincipal
// alone has no decider, so permission checks refuse it or evaluate it again.
func WithCaller(ctx context.Context, p kernel.Principal, d Decider) context.Context {
	return WithDecider(kernel.WithPrincipal(ctx, p), d)
}

// WithDecider returns ctx carrying d as the decider of ctx's principal,
// bound to kernel.PrincipalBinding(ctx): DeciderFrom answers with d only
// while that WithPrincipal call's principal is ctx's, so a context derived
// with a later kernel.WithPrincipal no longer sees it, and a context derived
// in any other way, such as with a deadline, does. A nil d, or one holding a
// nil pointer, is stored as no decider and hides a decider that ctx carries.
func WithDecider(ctx context.Context, d Decider) context.Context {
	if isNilDecider(d) {
		d = nil
	}
	return context.WithValue(
		ctx,
		deciderKey{},
		boundDecider{binding: kernel.PrincipalBinding(ctx), d: d},
	)
}

// DeciderFrom returns the decider that WithDecider bound to ctx's principal,
// and true. It returns nil and false when ctx carries no principal, when the
// decider was bound to a principal that a later WithPrincipal replaced, and
// when the bound decider is nil, which a caller answers as it does a request
// that was never authorised: by refusing, or by evaluating the principal
// with Authorizer.For.
func DeciderFrom(ctx context.Context) (Decider, bool) {
	b, ok := ctx.Value(deciderKey{}).(boundDecider)
	// Comparing the bindings with != is safe only because
	// kernel.PrincipalBinding returns the pointer that WithPrincipal stored:
	// pointers compare by identity, where an interface holding a value that
	// cannot be compared, such as a Principal with its slices, would panic.
	if !ok || b.d == nil || b.binding == nil || b.binding != kernel.PrincipalBinding(ctx) {
		return nil, false
	}
	return b.d, true
}

// isNilDecider reports whether d is nil or holds a nil pointer, map, slice,
// function or channel. Such a typed nil compares unequal to nil, so a caller
// that tested DeciderFrom's answer against nil would call through it.
func isNilDecider(d Decider) bool {
	if d == nil {
		return true
	}
	switch v := reflect.ValueOf(d); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}
