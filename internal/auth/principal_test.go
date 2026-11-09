package auth

import (
	"context"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authz"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitDeciderFrom(t *testing.T) {
	ctx := t.Context()
	if d := DeciderFrom(ctx); d != nil {
		t.Fatalf("a context without a principal has decider %p", d)
	}
	db := inmem.New()
	p, decider, err := Authorize(
		ctx,
		authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()},
		kernel.Principal{Name: "ann"},
	)
	if err != nil || decider == nil {
		t.Fatalf("authorize: %+v, %v", p, err)
	}
	parent := WithAuthorized(ctx, p, decider)
	if d := DeciderFrom(parent); d != decider {
		t.Fatalf("bound decider %p, want %p", d, decider)
	}
	derived, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	if d := DeciderFrom(derived); d != decider {
		t.Fatalf("a derived context sees %p, want %p", d, decider)
	}
	// The same name, but never authorised: matching principals by value
	// would let ann's decider answer for it.
	child := kernel.WithPrincipal(derived, kernel.Principal{Name: p.Name})
	if d := DeciderFrom(child); d != nil {
		t.Fatalf("a later WithPrincipal sees the earlier decider %p", d)
	}
	if d := DeciderFrom(parent); d != decider {
		t.Fatalf("the child changed its parent's decider to %p", d)
	}
	// An entry stored without a decider of its own, even one holding the
	// same principal, is not the entry the decider was bound to.
	rebound := kernel.WithPrincipal(parent, p)
	if d := DeciderFrom(rebound); d != nil {
		t.Fatalf("a decider answers for an entry it is not bound to: %p", d)
	}
	// ctx carries no principal, so the decider is bound to no entry.
	unbound := accessapi.WithDecider(ctx, authz.AsAPI(decider))
	if d := DeciderFrom(unbound); d != nil {
		t.Fatalf("a decider bound to no entry answers: %p", d)
	}
}

// WithAuthorized attaches a decider to the principal it stores, and only
// to that call: a later WithPrincipal hides it even when it stores the same
// principal, and a later WithAuthorized answers with its own decider.
func TestUnitWithAuthorized(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	src := authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	p, decider, err := Authorize(ctx, src, kernel.Principal{Name: "ann"})
	if err != nil || decider == nil {
		t.Fatalf("authorize: %+v, %v", p, err)
	}
	authorized := WithAuthorized(ctx, p, decider)
	if got, ok := kernel.FromContext(authorized); !ok || got.Name != p.Name {
		t.Fatalf("stored principal %+v, %v", got, ok)
	}
	if d := DeciderFrom(authorized); d != decider {
		t.Fatalf("attached decider %p, want %p", d, decider)
	}
	hidden := kernel.WithPrincipal(authorized, p)
	if got, ok := kernel.FromContext(hidden); !ok || got.Name != p.Name {
		t.Fatalf("principal stored again %+v, %v", got, ok)
	}
	if d := DeciderFrom(hidden); d != nil {
		t.Fatalf("a later WithPrincipal of the same principal sees decider %p", d)
	}
	_, again, err := Authorize(ctx, src, p)
	if err != nil || again == nil || again == decider {
		t.Fatalf("a second evaluation: %p, %v", again, err)
	}
	if d := DeciderFrom(WithAuthorized(hidden, p, again)); d != again {
		t.Fatalf("a later WithAuthorized sees %p, want %p", d, again)
	}
	if d := DeciderFrom(WithAuthorized(authorized, p, nil)); d != nil {
		t.Fatalf("a nil decider attached later still answers with %p", d)
	}
}

// One context key serves auth and access/api, so a decider stored through
// either is the one both read: a handler still on auth and a consumer
// already on access/api must agree on the request's evaluation.
func TestUnitDeciderSharedWithAccessAPI(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	p, decider, err := Authorize(
		ctx,
		authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()},
		kernel.Principal{Name: "ann", Groups: []string{"readers"}},
	)
	if err != nil || decider == nil {
		t.Fatalf("authorize: %+v, %v", p, err)
	}

	authorized := WithAuthorized(ctx, p, decider)
	if d, ok := accessapi.DeciderFrom(authorized); !ok || d != authz.AsAPI(decider) {
		t.Errorf("accessapi.DeciderFrom after WithAuthorized = %v, %v; want %p", d, ok, decider)
	}
	caller := accessapi.WithCaller(ctx, p, authz.AsAPI(decider))
	if d := DeciderFrom(caller); d != decider {
		t.Errorf("DeciderFrom after accessapi.WithCaller = %p, want %p", d, decider)
	}
	if got, ok := kernel.FromContext(caller); !ok || got.Name != p.Name {
		t.Errorf("principal after accessapi.WithCaller %+v, %v", got, ok)
	}

	// A nil evaluator reaches access/api as no decider at all, never as a
	// typed nil that a nil check would let through.
	if d, ok := accessapi.DeciderFrom(WithAuthorized(ctx, p, nil)); d != nil || ok {
		t.Errorf("accessapi.DeciderFrom after WithAuthorized with nil = %v, %v", d, ok)
	}
	// A decider that is not the evaluator, such as a test's fake, is no
	// evaluator to auth's callers, which then evaluate the principal afresh.
	if d := DeciderFrom(accessapi.WithCaller(ctx, p, notEvaluator{})); d != nil {
		t.Errorf("DeciderFrom with a fake decider = %p, want nil", d)
	}
}

// notEvaluator is an accessapi.Decider other than the evaluator.
type notEvaluator struct{ accessapi.Decider }
