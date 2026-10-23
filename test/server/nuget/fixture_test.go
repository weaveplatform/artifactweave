package nuget_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// Keep the protocol tests' fixture vocabulary local while sharing the seeded
// principals, request behavior and reproducible package bytes with root tests.
var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func cheapSeed(t *testing.T, db store.Store) { t.Helper(); support.Seed(t, db) }

func do(
	t *testing.T, h http.Handler, method, path string, headers map[string]string, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func basic(user, password string) map[string]string { return support.Basic(user, password) }
func bearerAuth(token string) map[string]string     { return support.Bearer(token) }
func nugetForm(pkg []byte) string                   { return support.NuGetForm(pkg) }

func contentTypeOf(body string) (contentType, rest string) {
	if b, ok := strings.CutPrefix(body, "multipart:"); ok {
		boundary, form, _ := strings.Cut(b, "\n")
		return "multipart/form-data; boundary=" + boundary, form
	}
	return "application/json", body
}

func nugetSymbolsPackage(id, version string) []byte {
	return support.NuGetSymbolsPackage(id, version)
}

func nugetPackage(id, version, more string) []byte {
	return support.NuGetPackage(id, version, more)
}

func artifactoryError(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	return support.ArtifactoryError(t, rec)
}
