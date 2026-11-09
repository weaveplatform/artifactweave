package httpkit_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

func TestUnitCallerChecks(t *testing.T) {
	bare := context.Background()
	admin := kernel.WithPrincipal(bare, kernel.Principal{Name: "admin", Admin: true})
	user := kernel.WithPrincipal(bare, kernel.Principal{Name: "deployer"})

	if err := httpkit.AdminOnly(admin); err != nil {
		t.Fatalf("admin: %v", err)
	}
	for ctx, want := range map[context.Context]error{
		bare: kernel.ErrUnauthenticated,
		user: kernel.ErrForbidden,
	} {
		err := httpkit.AdminOnly(ctx)
		if !errors.Is(err, want) || err.Error() != "repositories: "+want.Error() {
			t.Fatalf("AdminOnly = %v, want %v wrapped", err, want)
		}
	}

	if _, err := httpkit.Principal(bare); !errors.Is(err, kernel.ErrUnauthenticated) {
		t.Fatalf("Principal without a caller: %v", err)
	}
	if p, err := httpkit.Principal(user); err != nil || p.Name != "deployer" {
		t.Fatalf("Principal = %+v, %v", p, err)
	}

	if err := httpkit.Denied(kernel.Principal{Anonymous: true}); !errors.Is(err, kernel.ErrUnauthenticated) {
		t.Fatalf("Denied(anonymous) = %v", err)
	}
	if err := httpkit.Denied(kernel.Principal{Name: "deployer"}); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("Denied(user) = %v", err)
	}

	var e *apierror.Error
	if err := httpkit.BadRequestf("bad %s %d", "key", 7); !errors.As(err, &e) ||
		e.Status != http.StatusBadRequest || e.Message != "bad key 7" {
		t.Fatalf("BadRequestf = %v", err)
	}
}
