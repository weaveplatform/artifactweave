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
	p, err := Authorize(
		ctx,
		authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()},
		Principal{Name: "ann"},
	)
	if err != nil || p.Access() == nil {
		t.Fatalf("authorize: %+v, %v", p, err)
	}
	parent := WithPrincipal(ctx, p)
	if d := DeciderFrom(parent); d != p.Access() {
		t.Fatalf("bound decider %p, want %p", d, p.Access())
	}
	derived, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	if d := DeciderFrom(derived); d != p.Access() {
		t.Fatalf("a derived context sees %p, want %p", d, p.Access())
	}
	// The same name, but never authorised: matching principals by value
	// would let ann's decider answer for it.
	child := WithPrincipal(derived, Principal{Name: p.Name})
	if d := DeciderFrom(child); d != nil {
		t.Fatalf("a later WithPrincipal sees the earlier decider %p", d)
	}
	if d := DeciderFrom(parent); d != p.Access() {
		t.Fatalf("the child changed its parent's decider to %p", d)
	}
	// An entry stored without a decider of its own, even one holding the
	// same principal, is not the entry the decider was bound to.
	rebound := context.WithValue(parent, ctxKey{}, &principalEntry{p: p})
	if d := DeciderFrom(rebound); d != nil {
		t.Fatalf("a decider answers for an entry it is not bound to: %p", d)
	}
	unbound := context.WithValue(ctx, deciderKey{}, boundDecider{d: p.Access()})
	if d := DeciderFrom(unbound); d != nil {
		t.Fatalf("a decider bound to no entry answers: %p", d)
	}
}
