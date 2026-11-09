package server

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	accessgen "github.com/weaveplatform/artifactweave/migration/platform/gen/access"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

func TestAccAccessIdentityWithoutStore(t *testing.T) {
	ctx := kernel.WithPrincipal(context.Background(), kernel.Principal{Name: "admin", Admin: true})
	svc := AccessService{}
	if _, err := svc.GetUserList(
		ctx,
		accessgen.GetUserListRequestObject{},
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Errorf("users without a store: %v", err)
	}
	if _, err := svc.GetGroupList(
		ctx,
		accessgen.GetGroupListRequestObject{},
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Errorf("groups without a store: %v", err)
	}
	if _, err := svc.GetBasicAuthenticationStatus(
		ctx, accessgen.GetBasicAuthenticationStatusRequestObject{},
	); !errors.Is(err, apierror.ErrNotImplemented) {
		t.Errorf("the basic authentication switch without a store: %v", err)
	}
}
