package accesshttp

// This file holds the fixtures of the tests that call the middleware and the
// partials without the router, over test/server/support.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// errBoom is the error the test doubles fail with.
var errBoom = errors.New("boom")

// testHash is the cheap Argon2id cost of support.TestHash, which the stored
// hashes record, so verification costs as little.
var testHash = support.TestHash

// cheapSeed seeds db with the built-in principals, admin's password "pw".
func cheapSeed(t *testing.T, db *inmem.Store) {
	t.Helper()
	support.Seed(t, db)
}

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func basic(user, password string) map[string]string {
	return support.Basic(user, password)
}

// testAuthnOptions are the router's options for Authenticate (newAuthnOptions
// in app's router) as far as the tests' requests reach them: the system ping
// is a public route beside Access's, no path is the registry's or challenges
// anonymous requests, and skip are the caller's.
func testAuthnOptions(
	a *authn.Authenticator,
	st Store,
	skip ...func(*http.Request) bool,
) AuthnOptions {
	public := PublicRoutes()
	public["GET /artifactory/api/system/ping"] = true
	return AuthnOptions{
		Auth:               a,
		Store:              st,
		Skip:               skip,
		RegistryPath:       func(string) bool { return false },
		ChallengeAnonymous: func(string) bool { return false },
		PublicRoutes:       public,
	}
}

// asAdmin is ctx with the administrator as its principal, as the middleware
// leaves it for a request with admin's credentials.
func asAdmin(ctx context.Context) context.Context {
	return kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
}

// errStatus is the status the strict server answers an operation's error
// with: an apierror's own status, 500 for any other error, and 0 for none.
func errStatus(err error) int {
	var ae *apierror.Error
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ae):
		return ae.Status
	default:
		return http.StatusInternalServerError
	}
}
