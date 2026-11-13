package access_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

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
				t.Errorf("%s (anonymous access %v): %d %q %q, want %d %q %q", tc.name, anonymous,
					rec.Code, rec.Body, rec.Header().Get("WWW-Authenticate"), tc.status, tc.body, tc.auth)
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
		if rec := do(t, h, http.MethodGet, secAPIKey, headers, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s beside a wrong password: %d %s", header, rec.Code, rec.Body)
		}
	}
	rec := do(t, h, http.MethodGet, "/artifactory/api/security/lockedUsers", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK || rec.Body.String() != "[]" && rec.Body.String() != "[]\n" {
		t.Fatalf("locked users: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodGet, secAPIKey, basic("user", "pw"), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("user's password after the key: %d %s", rec.Code, rec.Body)
	}
}
