package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
)

// buildRouteRecorder records the build name and number each Builds
// operation receives, so the test sees what the generated router bound.
type buildRouteRecorder struct {
	artgen.UnimplementedStrict

	got []string
}

func (b *buildRouteRecorder) GetBuildRuns(
	_ context.Context, req artgen.GetBuildRunsRequestObject,
) (artgen.GetBuildRunsResponseObject, error) {
	b.got = append(b.got, "runs "+req.BuildName)
	return nil, apierror.ErrNotImplemented
}

func (b *buildRouteRecorder) GetBuildInfo(
	_ context.Context, req artgen.GetBuildInfoRequestObject,
) (artgen.GetBuildInfoResponseObject, error) {
	b.got = append(b.got, "info "+req.BuildName+" "+req.BuildNumber)
	return nil, apierror.ErrNotImplemented
}

func (b *buildRouteRecorder) PromoteBuild(
	_ context.Context, req artgen.PromoteBuildRequestObject,
) (artgen.PromoteBuildResponseObject, error) {
	b.got = append(b.got, "promote "+req.BuildName+" "+req.BuildNumber)
	return nil, apierror.ErrNotImplemented
}

// TestAccBuildRoutesKeepEscapedSlash pins research 22 §3.3: the JFrog CLI
// path-escapes a build name or number containing / as %2F. chi matches on the
// raw path and the generated binder unescapes the segment, so the operation
// receives the name whole. A router that matched on the decoded path would
// split it into two segments and answer 404 or call another operation.
func TestAccBuildRoutesKeepEscapedSlash(t *testing.T) {
	rec := &buildRouteRecorder{}
	r := chi.NewRouter()
	artgen.HandlerWithOptions(
		artgen.NewStrictHandler(rec, nil),
		artgen.ChiServerOptions{BaseRouter: r},
	)

	for _, tc := range []struct {
		method, target, body, want string
	}{
		{http.MethodGet, "/api/build/team%2Fapp", "", "runs team/app"},
		{http.MethodGet, "/api/build/team%2Fapp/42%2Frc1", "", "info team/app 42/rc1"},
		{http.MethodPost, "/api/build/promote/team%2Fapp/42", "{}", "promote team/app 42"},
	} {
		rec.got = nil
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
		if len(rec.got) != 1 || rec.got[0] != tc.want {
			t.Errorf(
				"%s %s: operations called %q, want [%q]",
				tc.method,
				tc.target,
				rec.got,
				tc.want,
			)
		}
	}
}
