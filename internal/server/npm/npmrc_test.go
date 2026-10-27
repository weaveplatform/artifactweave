package npm_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/npm"
)

// The lines follow JFrog's 7.x documentation less the email and always-auth
// lines 7.161.26 dropped: _auth with spaces around =, and, scoped, the
// registry line then _password and username keyed to the registry URL
// without its scheme. A token caller gets its token as _authToken.
func TestUnitNPMRCLines(t *testing.T) {
	const registry = "https://repo.example/artifactory/api/npm/npm-local/"
	basic := npm.Credential{User: "admin", Password: "cmVmdSecret"}
	bearer := npm.Credential{Token: "eyJ.tok.en"}
	for _, c := range []struct{ got, expected string }{
		{npm.AuthLines(basic), "_auth = YWRtaW46Y21WbWRTZWNyZXQ=\n"},
		{npm.AuthLines(bearer), "_authToken = eyJ.tok.en\n"},
		{
			npm.ScopeAuthLines("acme", registry, basic),
			"@acme:registry=" + registry + "\n" +
				"//repo.example/artifactory/api/npm/npm-local/:_password=Y21WbWRTZWNyZXQ=\n" +
				"//repo.example/artifactory/api/npm/npm-local/:username=admin\n",
		},
		{
			npm.ScopeAuthLines("acme", registry, bearer),
			"@acme:registry=" + registry + "\n" +
				"//repo.example/artifactory/api/npm/npm-local/:_authToken=eyJ.tok.en\n",
		},
	} {
		if c.got != c.expected {
			t.Errorf("got\n%s\nwant\n%s", c.got, c.expected)
		}
	}
}

// A scope is written into .npmrc, so only what npm accepts as one passes.
func TestUnitValidScope(t *testing.T) {
	for scope, want := range map[string]bool{
		"acme": true, "my-org.io": true, "": false, "a b": false, "a\nb": false,
		"a/b": false, "@acme": false, "_x": true,
	} {
		if got := npm.ValidScope(scope); got != want {
			t.Errorf("ValidScope(%q) = %v, want %v", scope, got, want)
		}
	}
}
