package configurationhttp

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// TestAccServiceIDWithoutIdentity: a store with no instance identity is a
// server fault, never an empty id a client would build a scope from.
func TestAccServiceIDWithoutIdentity(t *testing.T) {
	ctx := kernel.WithPrincipal(context.Background(), kernel.Principal{Name: "admin", Admin: true})
	_, err := SystemAPI{deps: Deps{Store: inmem.New()}}.
		GetServiceId(ctx, artgen.GetServiceIdRequestObject{})
	var e *apierror.Error
	if !errors.As(err, &e) || e.Status != http.StatusInternalServerError {
		t.Fatalf("err %v, want a 500", err)
	}
}
