package dockerhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type cancelledIdentity struct{ accessapi.Identity }

func (cancelledIdentity) User(context.Context, string) (accessapi.UserInfo, error) {
	return accessapi.UserInfo{}, context.Canceled
}

func TestUnitWriteAuthCancellationClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile apierror.Profile
	}{
		{"v1", apierror.ProfileArtifactory},
		{"v2", apierror.ProfileOCI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, cancelled := range []bool{true, false} {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if cancelled {
					cancel()
				}
				var cause error
				ctx = kernel.WithPrincipal(httpkit.WithCause(ctx, &cause), kernel.Principal{Name: "writer"})
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/auth", nil)
				r.Header.Set("Authorization", "Bearer private-fixture")
				g := &Registry{identity: cancelledIdentity{}}
				rec := httptest.NewRecorder()
				rec.Code = 0
				aborted := func() (caught any) {
					defer func() { caught = recover() }()
					g.writeAuth(rec, r, tc.profile)
					return nil
				}()
				if cancelled {
					if aborted != http.ErrAbortHandler { //nolint:errorlint // net/http requires this exact sentinel
						t.Fatal("cancelled Docker config lookup did not abort", aborted)
					}
					if rec.Code != 0 || rec.Body.Len() != 0 || !errors.Is(cause, context.Canceled) {
						t.Fatal("cancelled Docker config lookup published a response or lost its cause", rec.Code)
					}
				} else if aborted != nil || rec.Code != http.StatusInternalServerError {
					t.Fatal("live Docker config backend failure changed", rec.Code, aborted)
				}
			}
		})
	}
}
