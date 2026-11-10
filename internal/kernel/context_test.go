package kernel_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Access binds a request's permission decider to PrincipalBinding, so the
// binding must identify the WithPrincipal call rather than the principal's
// value: two calls with equal principals must differ, or a decider would
// answer for a principal it never evaluated, and a context derived for a
// deadline must keep it, or a handler would lose its decider.
func TestUnitPrincipalContext(t *testing.T) {
	ctx := t.Context()
	if p, ok := kernel.FromContext(ctx); ok || kernel.PrincipalBinding(ctx) != nil {
		t.Fatalf("a bare context has principal %+v, %v", p, ok)
	}
	ann := kernel.Principal{Name: "ann", Groups: []string{"readers"}}
	user := kernel.WithPrincipal(ctx, ann)
	if p, ok := kernel.FromContext(user); !ok || p.Name != "ann" {
		t.Fatalf("stored principal %+v, %v", p, ok)
	}
	binding := kernel.PrincipalBinding(user)
	if binding == nil {
		t.Fatal("a stored principal has no binding")
	}
	derived, cancel := context.WithTimeout(user, time.Minute)
	defer cancel()
	if kernel.PrincipalBinding(derived) != binding {
		t.Error("a context derived with a deadline has another binding")
	}
	if kernel.PrincipalBinding(kernel.WithPrincipal(derived, ann)) == binding {
		t.Error("a second WithPrincipal of the same principal shares the first binding")
	}
	if kernel.PrincipalBinding(user) != binding {
		t.Error("a later WithPrincipal changed its parent's binding")
	}

	// The 401 and 403 split Artifactory makes (decision 0005).
	anonymous := kernel.WithPrincipal(ctx, kernel.Anonymous)
	admin := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
	for name, tc := range map[string]struct {
		ctx              context.Context
		authn, adminOnly error
	}{
		"none":      {ctx, kernel.ErrUnauthenticated, kernel.ErrUnauthenticated},
		"anonymous": {anonymous, kernel.ErrUnauthenticated, kernel.ErrUnauthenticated},
		"user":      {user, nil, kernel.ErrForbidden},
		"admin":     {admin, nil, nil},
	} {
		if _, err := kernel.RequireAuthenticated(tc.ctx); !errors.Is(err, tc.authn) {
			t.Errorf("%s: RequireAuthenticated = %v, want %v", name, err, tc.authn)
		}
		if p, err := kernel.RequireAdmin(tc.ctx); !errors.Is(err, tc.adminOnly) ||
			(err == nil) != (p.Name == "admin") {
			t.Errorf("%s: RequireAdmin = %+v, %v, want %v", name, p, err, tc.adminOnly)
		}
	}
}
