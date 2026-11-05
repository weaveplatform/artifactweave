package auth

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/authz"
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
		Principal{Name: "ann"},
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
	child := WithPrincipal(derived, Principal{Name: p.Name})
	if d := DeciderFrom(child); d != nil {
		t.Fatalf("a later WithPrincipal sees the earlier decider %p", d)
	}
	if d := DeciderFrom(parent); d != decider {
		t.Fatalf("the child changed its parent's decider to %p", d)
	}
	// An entry stored without a decider of its own, even one holding the
	// same principal, is not the entry the decider was bound to.
	rebound := context.WithValue(parent, ctxKey{}, &principalEntry{p: p})
	if d := DeciderFrom(rebound); d != nil {
		t.Fatalf("a decider answers for an entry it is not bound to: %p", d)
	}
	unbound := context.WithValue(ctx, deciderKey{}, boundDecider{d: decider})
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
	p, decider, err := Authorize(ctx, src, Principal{Name: "ann"})
	if err != nil || decider == nil {
		t.Fatalf("authorize: %+v, %v", p, err)
	}
	authorized := WithAuthorized(ctx, p, decider)
	if got, ok := FromContext(authorized); !ok || got.Name != p.Name {
		t.Fatalf("stored principal %+v, %v", got, ok)
	}
	if d := DeciderFrom(authorized); d != decider {
		t.Fatalf("attached decider %p, want %p", d, decider)
	}
	hidden := WithPrincipal(authorized, p)
	if got, ok := FromContext(hidden); !ok || got.Name != p.Name {
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
