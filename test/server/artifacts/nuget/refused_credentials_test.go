package nuget_test

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TestAccNuGetClientRefusedPassword: a NuGet client whose password is
// refused gets 403 "Bad credentials" with the Basic challenge, where any
// other client gets 401, so that it stops asking for credentials, as 7.x's
// entry point answers it (ArtifactoryBasicAuthenticationEntryPoint.
// isAlreadyAuthedNuGetRequest, 7.161.26): a password that does not verify,
// a User-Agent naming NuGet in any case, and an Authorization or
// X-NuGet-ApiKey value that is not blank. A token that does not verify, a
// key without a password and a request without credentials keep their 401
// (#1465). Each refused password names a user of its own, since a failed
// login suspends its user for a while.
func TestAccNuGetClientRefusedPassword(t *testing.T) {
	f := newNugetKeyFixture(t)
	const (
		reindex  = "/artifactory/api/nuget/reindex-nuget/reindex"
		nugetCLI = "NuGet Command Line/6.11.1 (Microsoft Windows NT 10.0.22631.0)"
		dotnet   = "NuGet .NET Core MSBuild Task/6.11.1 (.NET 8.0.10; Linux)"
		refused  = `{"errors":[{"message":"Bad credentials","status":403}]}` + "\n"
		bad      = `{"errors":[{"message":"Bad credentials","status":401}]}` + "\n"
		required = `{"errors":[{"message":"Authentication is required","status":401}]}` + "\n"
	)
	agent := func(ua string, h map[string]string) map[string]string {
		out := map[string]string{"User-Agent": ua}
		for k, v := range h {
			out[k] = v
		}
		return out
	}
	for _, c := range []struct {
		name, method, path string
		headers            map[string]string
		status             int
		body               string
	}{
		{
			"nuget.exe, a wrong Basic password", http.MethodPost, reindex,
			agent(nugetCLI, basic("u1", "wrong")), http.StatusForbidden, refused,
		},
		{
			"dotnet, a wrong X-NuGet-ApiKey password", http.MethodPost, reindex,
			agent(dotnet, map[string]string{"X-NuGet-ApiKey": "u2:wrong"}), http.StatusForbidden, refused,
		},
		// The agent is matched in any case, and on any route.
		{
			"an upper-case agent elsewhere", http.MethodGet, "/artifactory/api/repositories",
			agent("NUGET/6", basic("u3", "wrong")), http.StatusForbidden, refused,
		},
		{
			"another agent, a wrong Basic password", http.MethodPost, reindex,
			agent("curl/8.7.1", basic("u4", "wrong")), http.StatusUnauthorized, bad,
		},
		{
			"nuget.exe, no credentials", http.MethodPost, reindex,
			agent(nugetCLI, nil), http.StatusUnauthorized, required,
		},
		// A token that does not verify is not a refused password in 7.x
		// (AccessTokenAuthenticationException), and decision 0038 answers
		// a key without a password 401.
		{
			"nuget.exe, a Bearer token that does not verify", http.MethodPost, reindex,
			agent(nugetCLI, map[string]string{"Authorization": "Bearer eyJhbGciOiJSUzI1NiJ9.e30.c2ln"}),
			http.StatusUnauthorized, bad,
		},
		{
			"nuget.exe, a key without a colon", http.MethodPost, reindex,
			agent(nugetCLI, map[string]string{"X-NuGet-ApiKey": "u1wrong"}), http.StatusUnauthorized, bad,
		},
	} {
		rec := do(t, f.h, c.method, c.path, c.headers, "")
		if rec.Code != c.status || rec.Body.String() != c.body ||
			rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge {
			t.Errorf("%s: %d %q %q, want %d %q with the Basic challenge", c.name,
				rec.Code, rec.Body, rec.Header().Get("WWW-Authenticate"), c.status, c.body)
		}
	}
	// The right password still works for the agent.
	rec := do(t, f.h, http.MethodPost, reindex, agent(nugetCLI, basic("admin", "pw")), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin's password: %d %s", rec.Code, rec.Body)
	}
}
