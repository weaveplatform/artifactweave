package access_test

// This file holds the tests of the Basic values 7.x reads no credential from
// (#1451): "Basic Og==", a blank Basic value, and the Basic scheme spelled
// other than exactly "Basic ". 7.161.26's RequestUtils.getBasicAuthorization
// admits none of them, no other open source filter reads a Basic value, so
// AccessFilter.useAnonymousIfPossible serves them as a request without
// credentials: anonymous while anonymous access is on, the entry point's
// "Authentication is required" while it is off. JFrog meant it so for
// "Og==" (RTFACT-6765, 3.4.0). Whether a commercial filter reads them is not
// public [I].

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
)

// The file anonymous may read while anonymous access is on and "user" may
// not read at all, and the routes the tests call beside it.
const (
	ignoredFile    = "/artifactory/libs-local/ignored/a.txt"
	ignoredContent = "hello"
	ignoredPing    = "/artifactory/api/system/ping"
	ignoredAPIKey  = "/artifactory/api/security/apiKey"
)

// encoded is base64 of user:secret, as a Basic value carries it.
func encoded(user, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + secret))
}

// ignoredValues are the Authorization values 7.x reads no credential from,
// some of which name a user whose password is right, so that a request
// answered as that user could not pass for one answered as anonymous.
func ignoredValues() map[string]string {
	return map[string]string{
		"Basic Og==":            "Basic Og==",
		"blank Basic":           "Basic",
		"lower-case, anonymous": "basic " + encoded("anonymous", "x"),
		"lower-case, user":      "basic " + encoded("user", "pw"),
		"upper-case, admin":     "BASIC " + encoded("admin", "pw"),
		"tab, admin":            "Basic\t" + encoded("admin", "pw"),
	}
}

// newIgnoredBasicWorld is the item world, with anonymous access on or off,
// an authenticator that verifies tokens, a reference token of admin's, the
// file ignoredFile, the permission that lets anonymous read it, and the
// repositories docker-local and nuget-local.
func newIgnoredBasicWorld(t *testing.T, anonymous bool) (*itemWorld, string) {
	t.Helper()
	var tokens *token.Service
	w := newItemWorld(t, anonymous, func(c *Config) {
		ctx := t.Context()
		if err := c.Store.Instance().Init(ctx, instance.InstanceIdentity{
			InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
		var err error
		if tokens, err = token.New(ctx, c.Store, token.Options{KeyBits: 1024}); err != nil {
			t.Fatal(err)
		}
		c.Tokens = tokens
		if c.Auth, err = authn.NewAuthenticator(c.Store.Users(), c.Store.Settings(), authn.Options{
			Hash: testHash, Tokens: tokens,
		}); err != nil {
			t.Fatal(err)
		}
		for _, cfg := range []repo.Config{
			{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
			{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		} {
			if err := c.Store.Repos().Create(ctx, *repo.Defaults(&cfg)); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Store.Permissions().Create(ctx, accesstest.Permission{
			Name: "anonymous-libs",
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{
					kernel.AnonymousUser: api.ActionRead,
				}},
				Targets: map[string]accesstest.Target{"libs-local": {Include: []string{"**"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	})
	wantStatus(t, w.do(http.MethodPut, ignoredFile, "admin", ignoredContent), http.StatusCreated)
	issued, err := tokens.Issue(t.Context(), token.Request{
		Username: "admin", IncludeReference: true, ForceRevocable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w, issued.ReferenceToken
}

// wantAnswer fails t unless rec has the status, the whole body and, for a
// 401, the Basic challenge.
func wantAnswer(
	t *testing.T,
	what string,
	rec *httptest.ResponseRecorder,
	status int,
	body string,
) {
	t.Helper()
	if rec.Code != status || rec.Body.String() != body {
		t.Errorf("%s: %d %q, want %d %q", what, rec.Code, rec.Body, status, body)
	}
	if got := rec.Header().Get("WWW-Authenticate"); status == http.StatusUnauthorized &&
		got != apierror.BasicChallenge {
		t.Errorf("%s: 401 with challenge %q", what, got)
	}
}

var (
	authRequired   = legacyError(http.StatusUnauthorized, "Authentication is required") + "\n"
	badCredentials = legacyError(http.StatusUnauthorized, "Bad credentials") + "\n"
)

// TestAccIgnoredBasicIsAnonymous reads a file that anonymous may read with
// each ignored value: served as anonymous while anonymous access is on, so
// even a lower-case value naming "user", who may not read it, gets the
// file, and answered "Authentication is required" while it is off. A
// public route answers whatever the value, as it does without one: the
// anonymous ping interceptor lets a ping through when no Basic header is
// present (AnonymousPingInterceptor, 7.161.26). A well-formed Basic
// credential keeps its answers.
func TestAccIgnoredBasicIsAnonymous(t *testing.T) {
	for _, anonymous := range []bool{true, false} {
		w, _ := newIgnoredBasicWorld(t, anonymous)
		for name, value := range ignoredValues() {
			rec := w.do(http.MethodGet, ignoredFile, "", "", "Authorization", value)
			if anonymous {
				wantAnswer(t, "anonymous on, "+name, rec, http.StatusOK, ignoredContent)
			} else {
				wantAnswer(t, "anonymous off, "+name, rec, http.StatusUnauthorized, authRequired)
			}
			rec = w.do(http.MethodGet, ignoredPing, "", "", "Authorization", value)
			wantAnswer(t, "ping, "+name, rec, http.StatusOK, "OK")
		}
		// Kept: a well-formed Basic credential is read and verified.
		rec := w.do(
			http.MethodGet,
			ignoredFile,
			"",
			"",
			"Authorization",
			"Basic "+encoded("user", "wrong"),
		)
		wantAnswer(t, "wrong password", rec, http.StatusUnauthorized, badCredentials)
		rec = w.do(
			http.MethodGet,
			ignoredPing,
			"",
			"",
			"Authorization",
			"Basic "+encoded("user", "wrong"),
		)
		wantAnswer(t, "wrong password, ping", rec, http.StatusUnauthorized, badCredentials)
		rec = w.do(http.MethodGet, ignoredFile, "", "", "Authorization", "Basic "+encoded(":", ""))
		wantAnswer(
			t,
			"Basic of an empty name and a colon",
			rec,
			http.StatusUnauthorized,
			badCredentials,
		)
		rec = w.do(http.MethodGet, ignoredFile, "admin", "")
		wantAnswer(t, "right password", rec, http.StatusOK, ignoredContent)
	}
}

// TestAccIgnoredBasicCountsNoFailedLogin sends each kind of ignored value
// twice, enough to suspend a name it were counted against, and then the
// name's first well-formed login: "user" logs in, and anonymous and the
// empty name are answered as a first wrong password, not suspended.
func TestAccIgnoredBasicCountsNoFailedLogin(t *testing.T) {
	w, _ := newIgnoredBasicWorld(t, false)
	wantStatus(t, w.do(http.MethodPut, "/access/api/v1/config/security/user_lock_policy", "admin",
		`{"clientLoginSuspensionEnabled":true,"maxLoginDelayIncorrectAttempts":2,`+
			`"baseLoginDelayMillis":30000,"maxLoginDelayMillis":60000}`,
		"Content-Type", "application/json"), http.StatusOK)
	for _, c := range []struct {
		name, ignored, then string
		status              int
		body                string
	}{
		{
			"user", "basic " + encoded("user", "wrong"), "Basic " + encoded("user", "pw"),
			http.StatusNotFound, signedIn + "\n",
		},
		// The anonymous user has no password, so a well-formed login with
		// its name does not verify.
		{
			"anonymous", "basic " + encoded("anonymous", "x"), "Basic " + encoded("anonymous", "x"),
			http.StatusUnauthorized, badCredentials,
		},
		{"the empty name", "Basic Og==", "Basic " + encoded("", "pw"), http.StatusUnauthorized, badCredentials},
	} {
		for range 2 {
			rec := w.do(http.MethodGet, ignoredAPIKey, "", "", "Authorization", c.ignored)
			wantAnswer(t, c.name+", ignored", rec, http.StatusUnauthorized, authRequired)
		}
		rec := w.do(http.MethodGet, ignoredAPIKey, "", "", "Authorization", c.then)
		wantAnswer(t, c.name+", then well-formed", rec, c.status, c.body)
	}
}

// TestAccIgnoredBasicBesideAKey sends an ignored value beside another
// credential, which authenticates as if the value were not there: 7.x's
// props filter reads X-JFrog-Art-Api whatever the Authorization header says
// (AuthenticationFilterUtils.getTokenKeyValue, 7.161.26), and the NuGet
// routes read X-NuGet-ApiKey when no Authorization header counts [I]. The
// operations that hand a client its own credential back, npm's .npmrc
// lines and Docker's config.json entry, give the key that authenticated,
// never the ignored value's password.
func TestAccIgnoredBasicBesideAKey(t *testing.T) {
	w, ref := newIgnoredBasicWorld(t, false)
	lower := "basic " + encoded("admin", "pw")
	rec := w.do(http.MethodGet, ignoredAPIKey, "", "",
		"Authorization", "Basic Og==", "X-JFrog-Art-Api", ref)
	wantAnswer(t, "Og== and a key", rec, http.StatusNotFound, signedIn+"\n")
	rec = w.do(http.MethodGet, "/artifactory/api/npm/auth", "", "",
		"Authorization", lower, "X-JFrog-Art-Api", ref)
	wantAnswer(t, "npm auth", rec, http.StatusOK, "_authToken = "+ref+"\n")
	rec = w.do(http.MethodGet, "/artifactory/api/docker/docker-local/v2/auth", "", "",
		"Authorization", lower, "X-JFrog-Art-Api", ref)
	wantAnswer(t, "docker auth", rec, http.StatusOK,
		`{"https://reg.example":{"auth":"`+encoded("admin", ref)+`","email":""}}`+"\n")
	rec = w.do(http.MethodPost, "/artifactory/api/nuget/nuget-local/reindex", "", "",
		"Authorization", "Basic Og==", "X-NuGet-ApiKey", "admin:pw")
	wantAnswer(t, "NuGet key", rec, http.StatusOK,
		"NuGet reindex calculation for repository 'nuget-local' accepted.")
}

// TestAccIgnoredBasicOnTheRegistry sends "Basic Og==" to the Docker
// registry, the case JFrog fixed it for: Docker "as anonymous without
// ~/.dockercfg" (RTFACT-6765). The registry challenges it as a request
// without credentials, even while anonymous access is on (decision 0031),
// and the token endpoint issues the anonymous token while it is on. The
// registry's own filter is not public [I].
func TestAccIgnoredBasicOnTheRegistry(t *testing.T) {
	const (
		v2        = "/artifactory/api/docker/docker-local/v2/"
		challenge = `Bearer realm="https://reg.example/artifactory/api/docker/docker-local/v2/token",` +
			`service="reg.example"`
		required = `{"errors":[{"code":"UNAUTHORIZED","message":"Authentication is required"}]}` + "\n"
	)
	wantChallenge := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != required ||
			rec.Header().Get("WWW-Authenticate") != challenge {
			t.Errorf("%s: %d %q %q", what, rec.Code, rec.Body, rec.Header().Get("WWW-Authenticate"))
		}
	}
	for _, anonymous := range []bool{true, false} {
		w, _ := newIgnoredBasicWorld(t, anonymous)
		wantChallenge("v2", w.do(http.MethodGet, v2, "", "", "Authorization", "Basic Og=="))
		rec := w.do(
			http.MethodGet,
			v2+"token?scope=repository:x:pull",
			"",
			"",
			"Authorization",
			"Basic Og==",
		)
		if !anonymous {
			wantChallenge("token, anonymous off", rec)
			continue
		}
		var body struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
			body.Token == "" || body.Token != body.AccessToken {
			t.Fatalf("token, anonymous on: %d %s", rec.Code, rec.Body)
		}
		parts := strings.Split(body.Token, ".")
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || !strings.Contains(string(payload), `/users/anonymous"`) {
			t.Fatalf("token, anonymous on: payload %s, %v", payload, err)
		}
	}
}
