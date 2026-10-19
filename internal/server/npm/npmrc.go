package npm

import (
	"encoding/base64"
	"strings"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
)

// Credential is what GET /api/npm/auth and its scoped form write into
// .npmrc for a caller (decision 0034): a user name and the secret that
// stands for the password, for a caller that sent Basic credentials, or the
// token a caller sent as Bearer or X-JFrog-Art-Api.
type Credential struct {
	// User and Password are set for a Basic caller. Password is the
	// caller's encrypted password (DV-004) when it sent its password, and
	// the token it sent when it sent one as the password: never a password.
	User, Password string
	// Token is set for a token caller, to the token it sent.
	Token string
}

// AuthLines is GET /api/npm/auth's answer: _auth with the base64 of
// user:<encrypted password> for a Basic caller, as 7.x's documentation shows
// it, or _authToken for a token caller. 7.161.26 stopped sending the
// always-auth and email lines (RTDEV-96526).
func AuthLines(c Credential) string {
	if c.Token != "" {
		return "_authToken = " + c.Token + "\n"
	}
	return "_auth = " + base64.StdEncoding.EncodeToString([]byte(c.User+":"+c.Password)) + "\n"
}

// ScopeAuthLines is GET /api/npm/{repoKey}/auth/{scope}'s answer: the scope
// mapped to registry, the repository's registry URL ending in a slash, then
// the credential keyed to that URL less its scheme, so npm sends it to that
// registry alone. A Basic caller gets _password, the base64 of the
// encrypted password, and username, as 7.x's documentation shows them; the
// email and always-auth lines went in 7.161.26.
func ScopeAuthLines(scope, registry string, c Credential) string {
	key := registry
	if i := strings.Index(key, "//"); i >= 0 {
		key = key[i:]
	}
	var b strings.Builder
	b.WriteString("@" + scope + ":registry=" + registry + "\n")
	if c.Token != "" {
		b.WriteString(key + ":_authToken=" + c.Token + "\n")
		return b.String()
	}
	b.WriteString(
		key + ":_password=" + base64.StdEncoding.EncodeToString([]byte(c.Password)) + "\n",
	)
	b.WriteString(key + ":username=" + c.User + "\n")
	return b.String()
}

// ValidScope reports whether scope, without its @, can name an npm scope:
// npm accepts it in a scoped name. A scope is written into .npmrc, so
// anything else, a line break among it, is refused.
func ValidScope(scope string) bool { return npmfmt.ValidName("@" + scope + "/x") }
