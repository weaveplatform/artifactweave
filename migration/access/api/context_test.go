package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// stub is a Decider that answers nothing. A *stub is told apart by its
// address, and a stub held by value is a Decider that is not a pointer.
type stub struct{ id int }

func (stub) IsAdmin() bool                                               { return false }
func (stub) Groups() []string                                            { return nil }
func (stub) IsRepoVisible(api.Repo) bool                                 { return false }
func (stub) Allows(api.ResourceType, api.Repo, string, api.Actions) bool { return false }
func (stub) AllowsFolder(api.ResourceType, api.Repo, string, api.Actions) bool {
	return false
}
func (stub) System(string, kernel.ScopeActions) bool { return false }

// A request's decider answers only for the WithPrincipal call it was bound
// to. Were it matched to the principal's value instead, a decider would
// answer for a principal that no one evaluated; were a typed nil stored, a
// caller that tested the decider against nil would call through it.
func TestUnitDeciderContext(t *testing.T) {
	ctx := t.Context()
	first, second := &stub{1}, &stub{2}
	// Groups make the principal a value that == cannot compare, which the
	// binding comparison must never attempt.
	ann := kernel.Principal{Name: "ann", Groups: []string{"readers"}}

	from := func(t *testing.T, ctx context.Context, want api.Decider, why string) {
		t.Helper()
		got, ok := api.DeciderFrom(ctx)
		if got != want || ok != (want != nil) {
			t.Errorf("%s: DeciderFrom = %v, %v; want %v, %v", why, got, ok, want, want != nil)
		}
	}

	from(t, ctx, nil, "a context without a principal")
	from(t, api.WithDecider(ctx, first), nil, "a decider bound where no principal is stored")

	caller := api.WithCaller(ctx, ann, first)
	if p, ok := kernel.FromContext(caller); !ok || p.Name != "ann" {
		t.Fatalf("WithCaller stored principal %+v, %v", p, ok)
	}
	from(t, caller, first, "the caller's context")

	derived, cancel := context.WithTimeout(caller, time.Minute)
	defer cancel()
	from(t, derived, first, "a context derived with a deadline")
	from(t, api.WithDecider(derived, nil), nil, "a nil decider bound later")
	from(t, kernel.WithPrincipal(derived, ann), nil, "a later WithPrincipal of the same principal")
	from(t, api.WithCaller(derived, ann, second), second, "a later WithCaller")
	from(t, api.WithDecider(derived, second), second, "a decider bound later to the same principal")
	from(t, caller, first, "the caller's context after its children")

	var typedNil *stub
	from(t, api.WithDecider(caller, typedNil), nil, "a typed nil bound later")
	from(t, api.WithCaller(ctx, ann, typedNil), nil, "WithCaller with a typed nil")
	from(t, api.WithCaller(ctx, ann, nil), nil, "WithCaller with no decider")
	from(t, api.WithCaller(ctx, ann, stub{3}), stub{3}, "a decider held by value")
}
