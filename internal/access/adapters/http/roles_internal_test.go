package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestAccGlobalRolesWithoutStore(t *testing.T) {
	ctx := kernel.WithPrincipal(context.Background(), kernel.Principal{Name: "admin", Admin: true})
	if _, err := (AccessService{}).GetAllGlobalRoles(
		ctx,
		accessgen.GetAllGlobalRolesRequestObject{},
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Fatalf("a router without a store answers 501: %v", err)
	}
	if _, err := (AccessService{}).GetAllGlobalRoles(
		context.Background(),
		accessgen.GetAllGlobalRolesRequestObject{},
	); !errors.Is(
		err,
		kernel.ErrUnauthenticated,
	) {
		t.Fatalf("no principal: %v", err)
	}
}

func TestAccRoleErrorStatuses(t *testing.T) {
	s, ctx := AccessService{}, context.Background()
	for err, want := range map[error]int{
		fmt.Errorf("x: %w", roles.ErrNotFound): http.StatusNotFound,
		fmt.Errorf("x: %w", roles.ErrConflict): http.StatusTeapot,
		fmt.Errorf("x: %w", roles.ErrInvalid):  http.StatusBadRequest,
		fmt.Errorf("x: %w", roles.ErrBuiltin):  http.StatusBadRequest,
		fmt.Errorf("x: %w", roles.ErrInUse):    http.StatusBadRequest,
		errors.New("disk on fire"):             http.StatusInternalServerError,
	} {
		var e *apierror.Error
		if !errors.As(s.roleError(ctx, "op", err, http.StatusTeapot), &e) || e.Status != want {
			t.Errorf("%v: %+v, want %d", err, e, want)
		}
	}
}
