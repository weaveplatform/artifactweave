package access_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

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

// seededHandler is the router over a seeded in-memory store whose admin
// password is "pw".
func seededHandler(t *testing.T) (http.Handler, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: support.TestHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	return harness.Router(harness.Config{CompatVersion: "7.155.0", Store: db, Auth: a}), db
}

func basic(user, password string) map[string]string {
	return support.Basic(user, password)
}

func TestAccAuthentication(t *testing.T) {
	h, db := seededHandler(t)
	const (
		protected = "/artifactory/api/repositories"
		version   = "/artifactory/api/system/version"
	)
	type want struct {
		status    int
		challenge bool
		message   string
	}
	for name, tc := range map[string]struct {
		method, path string
		headers      map[string]string
		want         want
	}{
		"protected without credentials": {
			"GET", protected, nil,
			want{http.StatusUnauthorized, true, "Authentication is required"},
		},
		"public without credentials": {"GET", "/artifactory/api/system/ping", nil, want{status: http.StatusOK}},
		// Version is not public: Artifactory requires a valid user for it
		// (RTFACT-26414).
		"version without credentials": {
			"GET", version, nil,
			want{http.StatusUnauthorized, true, "Authentication is required"},
		},
		"version as admin": {"GET", version, basic("admin", "pw"), want{status: http.StatusOK}},
		"usage sink without credentials": {
			"POST", "/artifactory/api/system/usage", nil, want{status: http.StatusOK},
		},
		"wrong password on a public route": {
			"GET", "/artifactory/api/system/ping", basic("admin", "nope"),
			want{http.StatusUnauthorized, true, "Bad credentials"},
		},
		"unknown user": {
			"GET", protected, basic("ghost", "pw"),
			want{http.StatusUnauthorized, true, "Bad credentials"},
		},
		"admin": {"GET", protected, basic("admin", "pw"), want{status: http.StatusOK}},
		"bearer before tokens exist": {
			"GET", protected,
			map[string]string{"Authorization": "Bearer abc"},
			want{http.StatusUnauthorized, true, "Bad credentials"},
		},
		// A refused API key answers 403 with the challenge, as 7.x's entry
		// point answers it (isAlreadyAuthedApiKeyRequest, #1464).
		"api key header": {
			"GET", protected,
			map[string]string{"X-JFrog-Art-Api": "cmVmd123"},
			want{http.StatusForbidden, true, "Bad credentials"},
		},
	} {
		rec := do(t, h, tc.method, tc.path, tc.headers, "")
		if rec.Code != tc.want.status {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, tc.want.status, rec.Body)
			continue
		}
		if got := rec.Header().
			Get("WWW-Authenticate"); (got == apierror.BasicChallenge) != tc.want.challenge {
			t.Errorf("%s: challenge %q", name, got)
		}
		if tc.want.message != "" {
			var body struct {
				Errors []struct {
					Status  int    `json:"status"`
					Message string `json:"message"`
				} `json:"errors"`
			}
			if err := json.Unmarshal(
				rec.Body.Bytes(),
				&body,
			); err != nil || len(body.Errors) != 1 ||
				body.Errors[0].Message != tc.want.message ||
				body.Errors[0].Status != tc.want.status {
				t.Errorf("%s: body %s", name, rec.Body)
			}
		}
	}

	// /access answers in its own envelope.
	rec := do(t, h, "GET", "/access/api/v1/tokens", nil, "")
	if rec.Code != http.StatusUnauthorized ||
		rec.Body.String() != `{"errors":[{"code":"UNAUTHORIZED","message":"Authentication is required"}]}`+"\n" {
		t.Fatalf("access: %d %s", rec.Code, rec.Body)
	}

	// With anonymous access on, a request without credentials goes on as
	// anonymous; handlers decide what anonymous may do. The seeded Any
	// Remote grant covers no repository here, so the list is empty.
	if err := db.Settings().
		Put(context.Background(), settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, "GET", protected, nil, ""); rec.Code != http.StatusOK ||
		rec.Body.String() != "[]\n" {
		t.Fatalf("anonymous on: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", version, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("version, anonymous on: %d %s", rec.Code, rec.Body)
	}
}

// TestAccAPIKeyHeaders: an API key header is read before Authorization and
// alone, as 7.161.26's props filter, which comes before the Basic filter and
// accepts every request that sends a key, reads it: X-JFrog-Art-Api unless
// it is blank, otherwise X-Api-Key whenever it is sent
// (AuthenticationFilterUtils.getApiKeyTokenKeyValue). A key that does not
// verify answers 403 with the Basic challenge while either header is not
// blank (ArtifactoryBasicAuthenticationEntryPoint.isAlreadyAuthedApiKeyRequest),
// and a Basic password beside a key is never checked (#1464).
func TestAccAPIKeyHeaders(t *testing.T) {
	const (
		license   = "/artifactory/api/system/license"
		refused   = `{"errors":[{"message":"Bad credentials","status":403}]}` + "\n"
		forbidden = `{"errors":[{"message":"Forbidden","status":403}]}` + "\n"
		bad       = `{"errors":[{"message":"Bad credentials","status":401}]}` + "\n"
	)
	for _, anonymous := range []bool{false, true} {
		var tokens *token.Service
		h, secrets := newStatusFixture(t, anonymous, func(c *Config) { tokens = c.Tokens })
		ref := func(user string) string {
			t.Helper()
			issued, err := tokens.Issue(context.Background(), token.Request{
				Username: user, IncludeReference: true, ForceRevocable: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			return issued.ReferenceToken
		}
		adminKey, userKey := ref("admin"), ref("user")
		with := func(headers map[string]string, more ...string) map[string]string {
			out := map[string]string{}
			for k, v := range headers {
				out[k] = v
			}
			for i := 0; i+1 < len(more); i += 2 {
				out[more[i]] = more[i+1]
			}
			return out
		}
		// The license is an administrator's alone, so it tells who the
		// request acts as: admin gets 200 and user 403 "Forbidden".
		for _, tc := range []struct {
			name    string
			headers map[string]string
			status  int
			body    string
			auth    string
		}{
			{
				"admin's key beside Basic user:wrong",
				with(basic("user", "wrong"), "X-JFrog-Art-Api", adminKey),
				http.StatusOK, "", "",
			},
			{
				"user's key beside Basic admin:pw",
				with(basic("admin", "pw"), "X-JFrog-Art-Api", userKey),
				http.StatusForbidden, forbidden, "",
			},
			{
				"a key that does not verify beside Basic admin:pw",
				with(basic("admin", "pw"), "X-JFrog-Art-Api", "cmVmd123"),
				http.StatusForbidden, refused, apierror.BasicChallenge,
			},
			{
				"a key that does not verify beside Bearer",
				map[string]string{
					"Authorization": "Bearer " + secrets["short-access"], "X-JFrog-Art-Api": "cmVmd123",
				},
				http.StatusForbidden, refused, apierror.BasicChallenge,
			},
			{
				"an access token as the key",
				map[string]string{"X-JFrog-Art-Api": secrets["short-access"]},
				http.StatusForbidden, refused, apierror.BasicChallenge,
			},
			{
				"not a token as the key",
				map[string]string{"X-JFrog-Art-Api": "not-a-token"},
				http.StatusForbidden, refused, apierror.BasicChallenge,
			},
			{
				"X-Api-Key, admin's key",
				map[string]string{"X-Api-Key": adminKey},
				http.StatusOK, "", "",
			},
			{
				"X-Api-Key beside Basic user:pw",
				with(basic("user", "pw"), "X-Api-Key", adminKey),
				http.StatusOK, "", "",
			},
			{
				"X-Api-Key, not a token",
				map[string]string{"X-Api-Key": "not-a-token"},
				http.StatusForbidden, refused, apierror.BasicChallenge,
			},
			{
				"a blank X-JFrog-Art-Api gives way to X-Api-Key",
				map[string]string{"X-JFrog-Art-Api": " ", "X-Api-Key": adminKey},
				http.StatusOK, "", "",
			},
			{
				"X-JFrog-Art-Api comes before X-Api-Key",
				map[string]string{"X-JFrog-Art-Api": userKey, "X-Api-Key": adminKey},
				http.StatusForbidden, forbidden, "",
			},
			// 7.x reads an X-Api-Key sent empty as a key, which no provider
			// verifies, but its entry point answers 403 only for a key
			// header that is not blank, so this one answers 401 [I].
			{
				"X-Api-Key sent empty",
				map[string]string{"X-Api-Key": ""},
				http.StatusUnauthorized, bad, apierror.BasicChallenge,
			},
			{
				"X-Api-Key sent empty beside Basic admin:pw",
				with(basic("admin", "pw"), "X-Api-Key", ""),
				http.StatusUnauthorized, bad, apierror.BasicChallenge,
			},
		} {
			rec := do(t, h, http.MethodGet, license, tc.headers, "")
			if rec.Code != tc.status || tc.body != "" && rec.Body.String() != tc.body ||
				rec.Header().Get("WWW-Authenticate") != tc.auth {
				t.Errorf(
					"%s (anonymous access %v): %d %q %q, want %d %q %q",
					tc.name,
					anonymous,
					rec.Code,
					rec.Body,
					rec.Header().Get("WWW-Authenticate"),
					tc.status,
					tc.body,
					tc.auth,
				)
			}
		}
	}
}

// TestAccAPIKeyBesideAWrongPasswordCountsNothing: a Basic password sent
// beside an API key that verifies is never checked, as 7.x's Basic filter
// never sees a request its props filter accepted, so it counts as no failed
// login: with a lock after one wrong password, user is not locked (#1464).
func TestAccAPIKeyBesideAWrongPasswordCountsNothing(t *testing.T) {
	var tokens *token.Service
	h, _ := newStatusFixture(t, false, func(c *Config) { tokens = c.Tokens })
	issued, err := tokens.Issue(context.Background(), token.Request{
		Username: "user", IncludeReference: true, ForceRevocable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	admin := jsonHeaders(basic("admin", "pw"))
	if rec := do(t, h, http.MethodPut, "/access/api/v1/config/security/user_lock_policy", admin,
		`{"clientLoginSuspensionEnabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("suspension off: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPut, secLockPol, admin,
		`{"enabled":true,"loginAttempts":1}`); rec.Code != http.StatusOK {
		t.Fatalf("policy: %d %s", rec.Code, rec.Body)
	}
	for _, header := range []string{"X-JFrog-Art-Api", "X-Api-Key"} {
		headers := basic("user", "wrong")
		headers[header] = issued.ReferenceToken
		if rec := do(
			t,
			h,
			http.MethodGet,
			secAPIKey,
			headers,
			"",
		); rec.Code != http.StatusNotFound {
			t.Fatalf("%s beside a wrong password: %d %s", header, rec.Code, rec.Body)
		}
	}
	rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/security/lockedUsers",
		basic("admin", "pw"),
		"",
	)
	if rec.Code != http.StatusOK || rec.Body.String() != "[]" && rec.Body.String() != "[]\n" {
		t.Fatalf("locked users: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		secAPIKey,
		basic("user", "pw"),
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("user's password after the key: %d %s", rec.Code, rec.Body)
	}
}

// TestAccRefusedAccountsCarryTheChallenge: a locked account, a correct but
// expired password and a suspended login answer 403 with 7.x's Basic
// challenge, as its entry point's sendErrorResponse adds it to every answer
// it gives, the 403s of isUserLocked, isCredentialsExpired and
// isLoginDisabled included (ArtifactoryBasicAuthenticationEntryPoint,
// 7.161.26; #1485). The rule is the entry point's, so it holds on every
// route the middleware covers, each answering in its own envelope: the item
// route, the REST API, Docker's registry (whose own Bearer challenge is a
// 401's), npm, a NuGet client, Access, and pub, whose 403 already carried
// its Bearer realm="pub" challenge. A request of 7.x's UI, by its
// Request-Agent, gets none (UiRequestUtils.isUiRestRequest).
func TestAccRefusedAccountsCarryTheChallenge(t *testing.T) {
	const (
		locked  = "Login has failed. Due to Incorrect username/password or locked user."
		expired = "Password has expired"
		blocked = "This request is blocked due to recurrent login failures, please try again in 600 seconds"
	)
	admin := jsonHeaders(basic("admin", "pw"))
	expire := func(h http.Handler) {
		t.Helper()
		if rec := do(
			t,
			h,
			http.MethodPost,
			secExpire,
			admin,
			`["user"]`,
		); rec.Code != http.StatusOK {
			t.Fatalf("expire: %d %s", rec.Code, rec.Body)
		}
	}
	// One wrong password suspends user for ten minutes, so the message's
	// count of seconds cannot change while the test runs.
	suspend := func(h http.Handler) {
		t.Helper()
		if rec := do(t, h, http.MethodPut, "/access/api/v1/config/security/user_lock_policy", admin,
			`{"clientLoginSuspensionEnabled":true,"maxLoginDelayIncorrectAttempts":1,`+
				`"baseLoginDelayMillis":600000,"maxLoginDelayMillis":600000}`); rec.Code != http.StatusOK {
			t.Fatalf("policy: %d %s", rec.Code, rec.Body)
		}
		if rec := do(t, h, http.MethodGet, secAPIKey, basic("user", "wrong"), ""); rec.Code !=
			http.StatusUnauthorized {
			t.Fatalf("wrong password: %d %s", rec.Code, rec.Body)
		}
	}
	artifactory := func(m string) string { return `{"errors":[{"message":"` + m + `","status":403}]}` }
	routes := []struct {
		name, path, userAgent, challenge string
		body                             func(message string) string
	}{
		{
			"item",
			"/artifactory/libs-local/fixture/get.txt",
			"",
			apierror.BasicChallenge,
			artifactory,
		},
		{"api", "/artifactory/api/repositories", "", apierror.BasicChallenge, artifactory},
		{
			"docker", "/v2/", "", apierror.BasicChallenge,
			func(m string) string { return `{"errors":[{"code":"DENIED","message":"` + m + `"}]}` },
		},
		{
			"npm", "/artifactory/api/npm/npm-local/widget", "", apierror.BasicChallenge,
			func(m string) string { return `{"error":"` + m + `"}` },
		},
		{
			"nuget",
			"/artifactory/api/nuget/nuget-local/FindPackagesById()",
			"NuGet Command Line/6.11.0",
			apierror.BasicChallenge,
			artifactory,
		},
		{
			"access",
			"/access/api/v1/tokens",
			"",
			apierror.BasicChallenge,
			func(m string) string { return `{"errors":[{"code":"FORBIDDEN","message":"` + m + `"}]}` },
		},
		{
			"pub", "/artifactory/api/pub/libs-local/api/packages/widget", "", "pub",
			func(m string) string { return `{"error":{"code":"FORBIDDEN","message":"` + m + `"}}` },
		},
	}
	for _, c := range []struct {
		name, user, message string
		setup               func(http.Handler)
	}{
		{"locked", "locked", locked, nil},
		{"expired", "user", expired, expire},
		{"suspended", "user", blocked, suspend},
	} {
		for _, route := range routes {
			for _, ui := range []bool{false, true} {
				h := statusFixture(t, false)
				if c.setup != nil {
					c.setup(h)
				}
				headers := basic(c.user, "pw")
				if route.userAgent != "" {
					headers["User-Agent"] = route.userAgent
				}
				challenge := route.challenge
				if challenge == "pub" {
					challenge = `Bearer realm="pub", message="` + c.message + `"`
				}
				if ui {
					headers["Request-Agent"] = "artifactoryUI"
					challenge = ""
				}
				rec := do(t, h, http.MethodGet, route.path, headers, "")
				if rec.Code != http.StatusForbidden ||
					rec.Header().Get("WWW-Authenticate") != challenge ||
					rec.Body.String() != route.body(c.message)+"\n" {
					t.Errorf("%s on %s (UI %v): %d %q %s, want 403 %q %s",
						c.name, route.name, ui, rec.Code, rec.Header().Get("WWW-Authenticate"),
						rec.Body, challenge, route.body(c.message))
				}
			}
		}
	}
	// The 403s that do not come from the entry point keep no challenge: an
	// authenticated user without the right, and decision 0043's API
	// restriction.
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodGet, "/artifactory/api/system/license", basic("user", "pw"), "")
	if rec.Code != http.StatusForbidden || rec.Header().Get("WWW-Authenticate") != "" ||
		rec.Body.String() != artifactory("Forbidden")+"\n" {
		t.Errorf("a user without the right: %d %q %s",
			rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodPut,
		apiRestrictionPath+"user",
		admin,
		restrictionBody(),
	); rec.Code !=
		http.StatusOK {
		t.Fatalf("restrict: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/artifactory/api/repositories", basic("user", "pw"), "")
	if rec.Code != http.StatusForbidden || rec.Header().Get("WWW-Authenticate") != "" ||
		rec.Body.String() != artifactory("API access is temporarily restricted")+"\n" {
		t.Errorf("a restricted user: %d %q %s",
			rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body)
	}
}

// TestAccUIRequestsGetNoChallenge: 7.x's entry point sends its challenge
// with no answer to a request of its UI, a request URI under
// /artifactory/ui/ or a Request-Agent of exactly artifactoryUI
// (sendErrorResponse and UiRequestUtils.isUiRestRequest, 7.161.26), whatever
// the credential it refuses (#1485). A request without credentials while
// anonymous access is off is answered by AccessFilter itself, not the entry
// point's refusal, and keeps its challenge.
func TestAccUIRequestsGetNoChallenge(t *testing.T) {
	const api = "/artifactory/api/repositories"
	bad := func(status int, m string) string {
		return fmt.Sprintf(`{"errors":[{"message":%q,"status":%d}]}`+"\n", m, status)
	}
	ui := func(h map[string]string) map[string]string {
		out := map[string]string{"Request-Agent": "artifactoryUI"}
		for k, v := range h {
			out[k] = v
		}
		return out
	}
	// Each case has a router of its own: two wrong passwords would
	// suspend the name.
	for _, c := range []struct {
		name, path string
		headers    map[string]string
		status     int
		challenge  string
		body       string
	}{
		{"wrong password", api, ui(basic("admin", "wrong")), 401, "", bad(401, "Bad credentials")},
		{
			"bearer that does not verify", api,
			ui(map[string]string{"Authorization": "Bearer not-a-token"}), 401, "", bad(401, "Bad credentials"),
		},
		{
			"refused API key", api, ui(map[string]string{"X-JFrog-Art-Api": "cmVmd123"}),
			403, "", bad(403, "Bad credentials"),
		},
		{"wrong password, UI path", "/artifactory/ui/x", basic("admin", "wrong"), 401, "", bad(401, "Bad credentials")},
		{
			"another Request-Agent", api,
			map[string]string{
				"Request-Agent": "ArtifactoryUI", "Authorization": basic("admin", "wrong")["Authorization"],
			},
			401, apierror.BasicChallenge, bad(401, "Bad credentials"),
		},
		{
			"no credentials", api, ui(nil), 401, apierror.BasicChallenge,
			bad(401, "Authentication is required"),
		},
	} {
		rec := do(t, statusFixture(t, false), http.MethodGet, c.path, c.headers, "")
		if rec.Code != c.status || rec.Header().Get("WWW-Authenticate") != c.challenge ||
			rec.Body.String() != c.body {
			t.Errorf("%s: %d %q %s, want %d %q %s", c.name, rec.Code,
				rec.Header().Get("WWW-Authenticate"), rec.Body, c.status, c.challenge, c.body)
		}
	}
}
