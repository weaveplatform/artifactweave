package artifacts_test

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// restAnswer is a REST request and its answer: the status and the whole
// body.
type restAnswer struct {
	method, path, as string
	status           int
	body             string
}

// wantRestAnswers fails t unless each request answers as given, with the
// Basic challenge on a 401 alone.
func wantRestAnswers(t *testing.T, w *itemWorld, answers []restAnswer) {
	t.Helper()
	for _, a := range answers {
		rec := w.do(a.method, a.path, a.as, "")
		body := a.body
		if rec.Code != a.status || rec.Body.String() != body {
			t.Errorf("%s %s as %q: %d %q, want %d %q", a.method, a.path, a.as, rec.Code, rec.Body, a.status, body)
		}
		challenge := rec.Header().Get("WWW-Authenticate")
		if a.status == http.StatusUnauthorized && challenge != basicChallenge ||
			a.status != http.StatusUnauthorized && challenge != "" {
			t.Errorf("%s %s as %q: challenge %q", a.method, a.path, a.as, challenge)
		}
	}
}

// TestAccGlobalHideRESTSurfaces (#1908): the security configuration's
// hideUnauthorizedResources, switched by PATCH on a running server, hides
// the REST refusals 7.161.26 hides, each in its own answer:
//   - the storage item info and statistics: 404 "Resource not found", to
//     an anonymous caller too (ArtifactResource.prepareUnAuthorizedResponse
//     300-308);
//   - the latest version search to an anonymous caller: 404 "Not Found",
//     RestResponseFilter's filling of the bare 404
//     VersionsResources.getLatestVersionInternal builds (98-108);
//   - a permission target v2 the caller does not manage, on a read, an
//     existence check and a replace: 404 with an empty message
//     (RestSecurityRequestHandlerV2Impl.throwForbiddenPermissionErrorMessage
//     1011-1018);
//   - a path deletion without Delete: ARTIFACT_NOT_FOUND's 404
//     (RepositoryServiceImpl.assertNonRootPathDelete 3428-3444);
//   - the anonymous refusals the REST exception mappers answer with their
//     challenge: 404 "Not Found", as RestResponseFilter fills in
//     interceptForbiddenStatus's bare 404 (84-93).
//
// A named user's refusal by the mappers, the deletion of a repository's
// root, a route outside the mapped REST roots and a package format's
// endpoint keep their answers, and switching the setting off brings every
// refusal back.
func TestAccGlobalHideRESTSurfaces(t *testing.T) {
	w := virtualStatusWorld(t, true)
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "npm-local", Class: repo.Local, PackageType: repo.NPM,
	})); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodPut, "/artifactory/api/v2/security/permissions/pt", "admin",
		`{"name":"pt","repo":{"repositories":["a-local"],"actions":{"users":{"deployer":["read"]}}}}`,
		"Content-Type", "application/json"), http.StatusOK)
	patch := func(on string) {
		t.Helper()
		wantStatus(t, w.do(http.MethodPatch, "/artifactory/api/system/configuration", "admin",
			"security:\n  hideUnauthorizedResources: "+on+"\n", "Content-Type", "application/yaml"), http.StatusOK)
	}
	const (
		storage  = "/artifactory/api/storage/a-local/a-only.bin"
		target   = "/artifactory/api/v2/security/permissions/pt"
		versions = "/artifactory/api/versions/a-local/_any"
	)
	forbidden := func(user string) string {
		return "Request for 'a-local:a-only.bin' is forbidden for user: '" + user + "'."
	}
	const deletion = "Artifact deletion error: Could not locate artifact. Path: 'a-local/a-only.bin'"
	unchanged := []restAnswer{
		// The mappers pass a named user's forbidden through.
		{http.MethodGet, "/artifactory/api/security/users", "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, "Forbidden")},
		{http.MethodGet, storage + "?lastModified", "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, "Forbidden")},
		// isForbiddenRootPathDelete never hides.
		{http.MethodDelete, "/artifactory/a-local/", "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, "Forbidden")},
		// Outside the mapped roots, and a package format's endpoint.
		{http.MethodGet, "/artifactory/api/securityconfig", "", http.StatusUnauthorized, errorBody(t, http.StatusUnauthorized, "Authentication is required")},
		npmRefusal(t, w, "/artifactory/api/npm/npm-local/pkg"),
	}
	refusals := []restAnswer{
		{http.MethodGet, storage, "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, forbidden("user"))},
		{http.MethodGet, storage + "?stats", "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, forbidden("user"))},
		{http.MethodGet, storage + "?stats", "", http.StatusForbidden, errorBody(t, http.StatusForbidden, forbidden("anonymous"))},
		{http.MethodGet, versions, "", http.StatusUnauthorized, errorBody(t, http.StatusUnauthorized, "Authentication is required")},
		{http.MethodGet, target, "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, "Forbidden")},
		{http.MethodGet, target, "", http.StatusUnauthorized, errorBody(t, http.StatusUnauthorized, "Authentication is required")},
		{http.MethodDelete, "/artifactory/a-local/a-only.bin", "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, "Forbidden")},
		{http.MethodDelete, "/artifactory/a-local/a-only.bin", "", http.StatusUnauthorized, errorBody(t, http.StatusUnauthorized, "Authentication is required")},
		{http.MethodDelete, "/artifactory/up/c.bin", "user", http.StatusForbidden, errorBody(t, http.StatusForbidden, "Forbidden")},
		{http.MethodGet, storage + "?lastModified", "", http.StatusUnauthorized, errorBody(t, http.StatusUnauthorized, "Unauthorized")},
		{http.MethodGet, "/artifactory/api/security/users", "", http.StatusUnauthorized, errorBody(t, http.StatusUnauthorized, "Authentication is required")},
		{http.MethodGet, "/artifactory/api/storageinfo", "", http.StatusUnauthorized, errorBody(t, http.StatusUnauthorized, "Authentication is required")},
	}
	hidden := []restAnswer{
		{http.MethodGet, storage, "user", http.StatusNotFound, errorBody(t, http.StatusNotFound, "Resource not found")},
		{http.MethodGet, storage + "?stats", "user", http.StatusNotFound, errorBody(t, http.StatusNotFound, "Resource not found")},
		{http.MethodGet, storage + "?stats", "", http.StatusNotFound, errorBody(t, http.StatusNotFound, "Resource not found")},
		{http.MethodGet, versions, "", http.StatusNotFound, errorBody(t, http.StatusNotFound, "Not Found")},
		{http.MethodGet, target, "user", http.StatusNotFound, errorBody(t, http.StatusNotFound, "")},
		{http.MethodGet, target, "", http.StatusNotFound, errorBody(t, http.StatusNotFound, "")},
		{http.MethodDelete, "/artifactory/a-local/a-only.bin", "user", http.StatusNotFound, errorBody(t, http.StatusNotFound, deletion)},
		{http.MethodDelete, "/artifactory/a-local/a-only.bin", "", http.StatusNotFound, errorBody(t, http.StatusNotFound, deletion)},
		// A remote's file is deleted from its cache, which names it.
		{http.MethodDelete, "/artifactory/up/c.bin", "user", http.StatusNotFound, errorBody(t, http.StatusNotFound,
			"Artifact deletion error: Could not locate artifact. Path: 'up-cache/c.bin'")},
		{http.MethodGet, storage + "?lastModified", "", http.StatusNotFound, errorBody(t, http.StatusNotFound, "Not Found")},
		{http.MethodGet, "/artifactory/api/security/users", "", http.StatusNotFound, errorBody(t, http.StatusNotFound, "Not Found")},
		{http.MethodGet, "/artifactory/api/storageinfo", "", http.StatusNotFound, errorBody(t, http.StatusNotFound, "Not Found")},
	}
	wantRestAnswers(t, w, append(refusals, unchanged...))
	wantHeadAndPut(t, w, target, http.StatusForbidden)
	patch("true")
	wantRestAnswers(t, w, append(hidden, unchanged...))
	wantHeadAndPut(t, w, target, http.StatusNotFound)
	patch("false")
	wantRestAnswers(t, w, append(refusals, unchanged...))
}

// npmRefusal is the npm registry's anonymous refusal of path as it
// answers it before the setting is switched: a 401 with its challenge, in
// the registry's own envelope, which the setting must leave alone.
func npmRefusal(t *testing.T, w *itemWorld, path string) restAnswer {
	t.Helper()
	rec := w.do(http.MethodGet, path, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	return restAnswer{http.MethodGet, path, "", http.StatusUnauthorized, rec.Body.String()}
}

// wantHeadAndPut checks the permission target's existence check and a
// replace by a user who does not manage it, which answer as its read.
func wantHeadAndPut(t *testing.T, w *itemWorld, target string, status int) {
	t.Helper()
	if rec := w.do(http.MethodHead, target, "user", ""); rec.Code != status {
		t.Errorf("HEAD %s: %d, want %d", target, rec.Code, status)
	}
	rec := w.do(http.MethodPut, target, "user",
		`{"name":"pt","repo":{"repositories":["a-local"],"actions":{"users":{"user":["read"]}}}}`,
		"Content-Type", "application/json")
	want := errorBody(t, http.StatusForbidden, "Forbidden")
	if status == http.StatusNotFound {
		want = errorBody(t, http.StatusNotFound, "")
	}
	if rec.Code != status || rec.Body.String() != want {
		t.Errorf("PUT %s: %d %s, want %d %s", target, rec.Code, rec.Body, status, want)
	}
}
