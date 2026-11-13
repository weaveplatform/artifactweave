package p2_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccP2ForceAuthenticationOnEveryMethod: a P2 virtual repository with
// forceP2Authentication on answers an anonymous ?trace, PUT, DELETE and a
// method the item routes do not serve 401 "Unauthorized" with the Basic
// challenge, as it answers an anonymous GET, because 7.x's
// force-authentication filter runs before the request reaches the
// repository, whatever the method or query (OssForceAuthenticationFilter,
// 7.161.26). With anonymous access off, a request without credentials gets
// the same answer. Signed-in callers, and anonymous on the same virtual
// repository with the flag off, keep their answers. An empty Authorization
// header, which the filter counts as a credential and passes on, gets what
// authentication gives a request without credentials (#1435).
func TestAccP2ForceAuthenticationOnEveryMethod(t *testing.T) {
	on := true
	for _, anonymous := range []bool{true, false} {
		w := newItemWorld(t, anonymous)
		for _, c := range []repo.Config{
			{Key: "p2-public", Class: repo.Local, PackageType: repo.Generic},
			{
				Key: "p2-forced-virtual", Class: repo.Virtual, PackageType: repo.P2,
				Repositories: []string{"p2-public"}, ForceP2Authentication: &on,
			},
			{
				Key: "p2-open-virtual", Class: repo.Virtual, PackageType: repo.P2,
				Repositories: []string{"p2-public"},
			},
		} {
			if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		index := p2Fixture(t, "p2.index")
		if res := w.do(
			http.MethodPut,
			"/artifactory/p2-public/p2.index",
			"admin",
			string(index),
		); res.Code != http.StatusCreated {
			t.Fatalf("publish: %d %s", res.Code, res.Body)
		}
		grantAnonymous(t, w, "p2-forced-virtual", "p2-open-virtual", "p2-public")
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/artifactory/p2-forced-virtual/p2.index?trace"},
			{http.MethodPut, "/artifactory/p2-forced-virtual/p2-public/plugins/a.jar"},
			{http.MethodDelete, "/artifactory/p2-forced-virtual/p2-public/p2.index"},
			{http.MethodPost, "/artifactory/p2-forced-virtual/p2.index"},
		} {
			wantForced(t, tc.method+" "+tc.path, w.do(tc.method, tc.path, "", "x"))
		}
		// An empty Authorization header is a credential to 7.x's filter
		// (AuthenticationFilterUtils.isAuthHeaderPresent), so it passes the
		// request on, and authentication answers it as it answers no
		// credentials: anonymous reads the index while anonymous access is
		// on (AccessFilter.useAnonymousIfPossible), and "Authentication is
		// required" answers while it is off.
		const indexPath = "/artifactory/p2-forced-virtual/p2.index"
		empty := w.do(http.MethodGet, indexPath, "", "", "Authorization", "")
		wantCode, wantBody := http.StatusOK, "version"
		if !anonymous {
			wantCode, wantBody = http.StatusUnauthorized, `"message":"Authentication is required"`
		}
		if empty.Code != wantCode || !strings.Contains(empty.Body.String(), wantBody) {
			t.Errorf("an empty Authorization header, anonymous access %v: %d %q, want %d with %q",
				anonymous, empty.Code, empty.Body, wantCode, wantBody)
		}
		if !anonymous {
			continue
		}
		for _, tc := range []struct {
			what, method, path, as string
			code                   int
			body                   string
		}{
			{
				"admin trace", http.MethodGet, "/artifactory/p2-forced-virtual/p2.index?trace", "admin",
				http.StatusOK, "User: admin",
			},
			{
				"admin PUT", http.MethodPut, "/artifactory/p2-forced-virtual/plugins/a.jar", "admin",
				http.StatusMethodNotAllowed, "P2 repositories are remote or virtual",
			},
			{
				"admin DELETE", http.MethodDelete, "/artifactory/p2-forced-virtual/p2.index", "admin",
				http.StatusMethodNotAllowed, "Cannot delete from the virtual repository 'p2-forced-virtual'",
			},
			{
				"anonymous trace, flag off", http.MethodGet, "/artifactory/p2-open-virtual/p2.index?trace", "",
				http.StatusOK, "User: anonymous",
			},
			{
				"anonymous PUT, flag off", http.MethodPut, "/artifactory/p2-open-virtual/plugins/a.jar", "",
				http.StatusMethodNotAllowed, "P2 repositories are remote or virtual",
			},
			{
				"anonymous DELETE, flag off", http.MethodDelete, "/artifactory/p2-open-virtual/p2.index", "",
				http.StatusMethodNotAllowed, "Cannot delete from the virtual repository 'p2-open-virtual'",
			},
			{
				"anonymous POST, flag off", http.MethodPost, "/artifactory/p2-open-virtual/p2.index", "",
				http.StatusMethodNotAllowed, "method not allowed: POST /artifactory/p2-open-virtual/p2.index",
			},
		} {
			res := w.do(tc.method, tc.path, tc.as, "x")
			if res.Code != tc.code || !strings.Contains(res.Body.String(), tc.body) {
				t.Errorf(
					"%s: %d %q, want %d with %q",
					tc.what,
					res.Code,
					res.Body,
					tc.code,
					tc.body,
				)
			}
		}
	}
}
