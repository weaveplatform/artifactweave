package maven_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// forcedBody is the whole body of the 401 a forced Maven virtual
// repository answers a request without credentials: 7.x's
// force-authentication filter's ErrorResponse(401, "Unauthorized")
// (OssForceAuthenticationFilter, 7.161.26; [J] RTFACT-30189 shows it from
// 7.71.8) in the Artifactory envelope.
const forcedBody = `{"errors":[{"message":"Unauthorized","status":401}]}` + "\n"

// wantForced fails the test unless rec is the forced-authentication 401:
// the status, the Basic challenge and, but for a HEAD, the exact body.
func wantForced(t *testing.T, method, path string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized ||
		rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge ||
		(method != http.MethodHead && rec.Body.String() != forcedBody) {
		t.Errorf("%s %s: %d %v %q, want 401 with the Basic challenge and %q",
			method, path, rec.Code, rec.Header(), rec.Body, forcedBody)
	}
}

// wantAnswer fails the test unless rec has status code and a body that
// contains body.
func wantAnswer(t *testing.T, what string, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code || !strings.Contains(rec.Body.String(), body) {
		t.Errorf("%s: %d %q, want %d with %q", what, rec.Code, rec.Body, code, body)
	}
}

// forcedMavenWorld is an item world with a Maven local repository holding
// org/a/1.0/a-1.0.jar, two Maven virtual repositories over it,
// mvn-forced with forceMavenAuthentication on and mvn-open with it off,
// mvn-outer, an unforced Maven virtual repository over mvn-forced, and a
// Gradle, an Ivy and an SBT virtual repository with the flag on over a
// local repository of their type holding the same file. Anonymous may read,
// deploy to and delete from all of them.
func forcedMavenWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous)
	on := true
	configs := []repo.Config{
		{Key: "mvn-local", Class: repo.Local, PackageType: repo.Maven},
		{
			Key: "mvn-forced", Class: repo.Virtual, PackageType: repo.Maven,
			Repositories: []string{"mvn-local"}, DefaultDeploymentRepo: "mvn-local",
			ForceMavenAuthentication: &on,
		},
		{
			Key: "mvn-open", Class: repo.Virtual, PackageType: repo.Maven,
			Repositories: []string{"mvn-local"}, DefaultDeploymentRepo: "mvn-local",
		},
		{
			Key: "mvn-outer", Class: repo.Virtual, PackageType: repo.Maven,
			Repositories: []string{"mvn-forced"},
		},
	}
	for _, kind := range []repo.PackageType{repo.Gradle, repo.Ivy, repo.SBT} {
		local := string(kind) + "-local"
		configs = append(configs,
			repo.Config{Key: local, Class: repo.Local, PackageType: kind},
			repo.Config{
				Key: string(kind) + "-forced", Class: repo.Virtual, PackageType: kind,
				Repositories: []string{local}, ForceMavenAuthentication: &on,
			})
	}
	targets := map[string]accesstest.Target{}
	for _, c := range configs {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		targets[c.Key] = accesstest.Target{Include: []string{"**"}}
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "maven-anonymous",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				kernel.AnonymousUser: api.ActionRead | api.ActionWrite | api.ActionDelete,
			}},
			Targets: targets,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, local := range []string{"mvn-local", "gradle-local", "ivy-local", "sbt-local"} {
		w.put("/artifactory/"+local+"/org/a/1.0/a-1.0.jar", "jar")
	}
	return w
}

const forcedJar = "/artifactory/mvn-forced/org/a/1.0/a-1.0.jar"

// TestAccMavenForceAuthenticationOnAVirtual: with anonymous access on, a
// Maven virtual repository with forceMavenAuthentication on answers every
// anonymous request to its item paths 401 "Unauthorized" with the Basic
// challenge, though anonymous may read, deploy and delete there: GET, HEAD,
// ?trace, PUT, DELETE and a method the item routes do not serve, a file, a
// missing file, the repository's root with and without its slash, and a key
// with matrix parameters. The PUT stores nothing. Signed-in callers, and
// anonymous on the same virtual repository with the flag off, keep their
// answers, as does the package API route over the forced repository. The
// anonymous user's Basic credentials and a Bearer token that does not
// verify count as none on the forced repository, as in 7.x's filter while
// anonymous access is on; the credentials that filter passes on to
// authentication keep their answers
// (TestAccMavenForceAuthenticationPassesOtherCredentialsOn).
//
// Only the flag of the Maven virtual repository a request names counts. The
// forced repository read through an unforced one serves anonymous, as 7.x's
// filter reads the key from the request's path [I]; and a Gradle, an Ivy
// and an SBT virtual repository store the flag with no effect, as JFrog
// says it is "relevant for Maven virtual repositories only" and 7.x's
// filter checks the Maven type alone [I] (#1435).
func TestAccMavenForceAuthenticationOnAVirtual(t *testing.T) {
	w := forcedMavenWorld(t, true)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, forcedJar},
		{http.MethodHead, forcedJar},
		{http.MethodGet, forcedJar + "?trace"},
		{http.MethodPut, "/artifactory/mvn-forced/org/b/1.0/b-1.0.jar"},
		{http.MethodDelete, forcedJar},
		{http.MethodPost, forcedJar},
		{http.MethodGet, "/artifactory/mvn-forced/org/missing/1.0/missing-1.0.jar"},
		{http.MethodGet, "/artifactory/mvn-forced/"},
		{http.MethodGet, "/artifactory/mvn-forced"},
		{http.MethodGet, "/artifactory/mvn-forced;k=v/org/a/1.0/a-1.0.jar"},
	} {
		wantForced(t, tc.method, tc.path, w.do(tc.method, tc.path, "", "jar"))
	}
	if _, err := w.db.Items().
		Get(t.Context(), "mvn-local", false, "org/b/1.0/b-1.0.jar"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Errorf("a refused anonymous PUT stored its file: %v", err)
	}
	for _, tc := range []struct {
		what, method, path, as string
		code                   int
		body                   string
	}{
		{"admin GET", http.MethodGet, forcedJar, "admin", http.StatusOK, "jar"},
		{"admin trace", http.MethodGet, forcedJar + "?trace", "admin", http.StatusOK, "User: admin"},
		{"admin PUT", http.MethodPut, "/artifactory/mvn-forced/org/c/1.0/c-1.0.jar", "admin", http.StatusCreated, `"repo":"mvn-local"`},
		{
			"admin DELETE", http.MethodDelete, forcedJar, "admin", http.StatusMethodNotAllowed,
			"Cannot delete from the virtual repository 'mvn-forced'",
		},
		{"user without read", http.MethodGet, forcedJar, "user", http.StatusForbidden, `"message":"Forbidden"`},
		{"anonymous GET, flag off", http.MethodGet, "/artifactory/mvn-open/org/a/1.0/a-1.0.jar", "", http.StatusOK, "jar"},
		{
			"anonymous trace, flag off", http.MethodGet, "/artifactory/mvn-open/org/a/1.0/a-1.0.jar?trace", "",
			http.StatusOK, "User: anonymous",
		},
		{
			"anonymous PUT, flag off", http.MethodPut, "/artifactory/mvn-open/org/d/1.0/d-1.0.jar", "",
			http.StatusCreated, `"repo":"mvn-local"`,
		},
		{
			"anonymous DELETE, flag off", http.MethodDelete, "/artifactory/mvn-open/org/a/1.0/a-1.0.jar", "",
			http.StatusMethodNotAllowed, "Cannot delete from the virtual repository 'mvn-open'",
		},
		{
			"anonymous POST, flag off", http.MethodPost, "/artifactory/mvn-open/org/a/1.0/a-1.0.jar", "",
			http.StatusMethodNotAllowed, "method not allowed: POST /artifactory/mvn-open/org/a/1.0/a-1.0.jar",
		},
		{
			"anonymous storage API", http.MethodGet, "/artifactory/api/storage/mvn-forced/org/a/1.0/a-1.0.jar", "",
			http.StatusOK, `"repo":"mvn-forced"`,
		},
		{"through an unforced virtual", http.MethodGet, "/artifactory/mvn-outer/org/a/1.0/a-1.0.jar", "", http.StatusOK, "jar"},
		{"gradle virtual", http.MethodGet, "/artifactory/gradle-forced/org/a/1.0/a-1.0.jar", "", http.StatusOK, "jar"},
		{"ivy virtual", http.MethodGet, "/artifactory/ivy-forced/org/a/1.0/a-1.0.jar", "", http.StatusOK, "jar"},
		{"sbt virtual", http.MethodGet, "/artifactory/sbt-forced/org/a/1.0/a-1.0.jar", "", http.StatusOK, "jar"},
	} {
		wantAnswer(t, tc.what, w.do(tc.method, tc.path, tc.as, "jar"), tc.code, tc.body)
	}
	// Credentials that do not verify are still credentials: the
	// authentication's 401 answers them, as before.
	bad := w.do(
		http.MethodGet,
		forcedJar,
		"",
		"",
		"Authorization",
		basic("admin", "wrong")["Authorization"],
	)
	wantAnswer(t, "a wrong password", bad, http.StatusUnauthorized, `"message":"Bad credentials"`)
	// While anonymous access is on, the anonymous user's Basic credentials
	// and a Bearer token that does not verify count as none, as in 7.x's
	// filter (OssForceAuthenticationFilter.checkIfAuthenticationShouldBeIgnored,
	// 7.161.26). On an unforced repository the authentication answers them,
	// as before.
	const badCredentials = `"message":"Bad credentials"`
	open := "/artifactory/mvn-open/org/a/1.0/a-1.0.jar"
	for _, value := range []string{basic("anonymous", "")["Authorization"], "Bearer not-a-token"} {
		wantForced(t, http.MethodGet, forcedJar+" as "+value,
			w.do(http.MethodGet, forcedJar, "", "", "Authorization", value))
		rec := w.do(http.MethodGet, open, "", "", "Authorization", value)
		wantAnswer(t, "unforced, "+value, rec, http.StatusUnauthorized, badCredentials)
	}
	// A Basic value that does not decode to a user name, as Commons Codec
	// decodes it, counts as none too: the filter classes it as Basic and
	// reads a blank user name (RequestUtils.getBasicAuthorization and
	// getUsernameOrPasswordFromToken).
	wantForced(t, http.MethodGet, forcedJar+" as Basic !!!",
		w.do(http.MethodGet, forcedJar, "", "", "Authorization", "Basic !!!"))
}

// badCredentialsBody is the whole body of authentication's 401 for
// credentials that do not verify.
const badCredentialsBody = `{"errors":[{"message":"Bad credentials","status":401}]}` + "\n"

// TestAccMavenForceAuthenticationPassesOtherCredentialsOn: with anonymous
// access on, the credentials 7.x's force-authentication filter passes on to
// authentication keep the answers they get without the flag: "Basic Og=="
// and a lowercase "basic" of the anonymous user, which the filter does not
// class as Basic (RequestUtils.getBasicAuthorization, 7.161.26); an
// unpadded Basic value naming another user, which the filter decodes and
// net/http does not; an empty Authorization header, which the filter counts
// as present (AuthenticationFilterUtils.isAuthHeaderPresent); and an
// X-Api-Key header (getApiKeyTokenKeyValue). Authentication reads no
// credential from the first two either, as 7.x's chain does, and serves
// them as the anonymous user (#1451), as it does the last two; it answers
// the unpadded value 401 "Bad credentials". The world is fresh, so no
// earlier failed login suspends the anonymous user name (#1435).
func TestAccMavenForceAuthenticationPassesOtherCredentialsOn(t *testing.T) {
	w := forcedMavenWorld(t, true)
	for _, tc := range []struct {
		header, value string
		code          int
		body          string
	}{
		{"Authorization", "Basic Og==", http.StatusOK, "jar"},
		{
			"Authorization", "basic " + base64.StdEncoding.EncodeToString([]byte("anonymous:x")),
			http.StatusOK, "jar",
		},
		{
			"Authorization", "Basic " + base64.RawStdEncoding.EncodeToString([]byte("admin:wrong")),
			http.StatusUnauthorized, badCredentialsBody,
		},
		{"Authorization", "", http.StatusOK, "jar"},
		{"X-Api-Key", "key", http.StatusOK, "jar"},
	} {
		rec := w.do(http.MethodGet, forcedJar, "", "", tc.header, tc.value)
		if rec.Code != tc.code || rec.Body.String() != tc.body {
			t.Errorf("%s %q: %d %q, want %d %q",
				tc.header, tc.value, rec.Code, rec.Body, tc.code, tc.body)
		}
	}
}

// TestAccMavenForceAuthenticationWithAnonymousAccessOff: with anonymous
// access off, a request without credentials to a forced Maven virtual
// repository gets the filter's 401 "Unauthorized", where every other path
// keeps "Authentication is required", as 7.x's filter answers before its
// anonymous fallback refuses the request (AccessFilter.doFilterInternal,
// 7.161.26). Signed-in callers keep their answers (#1435). The anonymous
// user's Basic credentials and an empty Authorization header pass the
// filter, and the authentication answers them as no credentials: 7.x's
// Basic filter declines the anonymous user name while anonymous access is
// off (ArtifactoryBasicAuthenticationFilter.acceptFilter, #1451).
func TestAccMavenForceAuthenticationWithAnonymousAccessOff(t *testing.T) {
	w := forcedMavenWorld(t, false)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, forcedJar},
		{http.MethodGet, forcedJar + "?trace"},
		{http.MethodPut, "/artifactory/mvn-forced/org/b/1.0/b-1.0.jar"},
		{http.MethodDelete, forcedJar},
	} {
		wantForced(t, tc.method, tc.path, w.do(tc.method, tc.path, "", "jar"))
	}
	const required = `{"errors":[{"message":"Authentication is required","status":401}]}` + "\n"
	for _, path := range []string{
		"/artifactory/mvn-open/org/a/1.0/a-1.0.jar",
		"/artifactory/api/storage/mvn-forced/org/a/1.0/a-1.0.jar",
	} {
		if rec := w.do(http.MethodGet, path, "", ""); rec.Code != http.StatusUnauthorized ||
			rec.Body.String() != required {
			t.Errorf("anonymous %s: %d %q, want 401 %q", path, rec.Code, rec.Body, required)
		}
	}
	wantAnswer(t, "admin GET", w.do(http.MethodGet, forcedJar, "admin", ""), http.StatusOK, "jar")
	// With anonymous access off, the anonymous user's Basic credentials are
	// credentials like any other to the filter, which the authentication
	// declines and answers as no credentials (#1451).
	// An empty Authorization header is a credential to 7.x's filter
	// (AuthenticationFilterUtils.isAuthHeaderPresent), which authentication
	// answers as it answers no credentials.
	rec := w.do(http.MethodGet, forcedJar, "", "", "Authorization", "")
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != required {
		t.Errorf("an empty Authorization header: %d %q, want 401 %q", rec.Code, rec.Body, required)
	}
	anonymousBasic := basic("anonymous", "")["Authorization"]
	wantAnswer(t, "the anonymous user's Basic credentials",
		w.do(http.MethodGet, forcedJar, "", "", "Authorization", anonymousBasic),
		http.StatusUnauthorized, `"message":"Authentication is required"`)
}
