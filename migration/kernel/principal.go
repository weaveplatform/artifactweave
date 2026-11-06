package kernel

import "time"

// AnonymousUser is the name of the built-in anonymous user.
const AnonymousUser = "anonymous"

// Principal is who a request acts as. It holds no permissions: access's
// Authorize returns the decider that answers for it, and its WithAuthorized
// carries the two together in a request's context.
//
// PasswordRevision, IsTokenAuthenticated and TokenSubject are exported only
// because access, which sets them when it verifies a credential, is another
// package. Access trusts them when it rechecks that credential for a long
// request or for queued work, so a value set anywhere else could pass the
// recheck for a credential that was never verified. TestUnitPrincipalInternals
// (migration/layout) fails on any use outside access and the kernel.
type Principal struct {
	Name      string
	Admin     bool
	Anonymous bool
	Groups    []string
	// Scope is the scope of the access token the request presented; empty
	// for a password or anonymous request, which act with the user's full
	// permissions. Scopes only narrow (decision 0005); the permission
	// evaluator applies them.
	Scope Scope
	// TokenID is the id of the access or reference token the request
	// presented; empty for a password or anonymous request. The token API's
	// "me" resolves to it, and encryptedPassword refuses token callers.
	TokenID string
	// TokenExpires is when that token expires; zero for a password, an
	// anonymous request or a token that never expires. A registry token
	// obtained with a token lives no longer (decision 0005).
	TokenExpires time.Time
	// Registry is what a registry token grants, nil for any other
	// credential. Such a principal reaches the registry's routes alone.
	Registry *RegistryAccess
	// Identity is how the token's subject resolved to a user: transient or
	// synthetic for a token from an OIDC exchange, transient for one an
	// administrator minted for a name that is no stored user; a registry
	// token obtained with it keeps it (decision 0005).
	Identity TokenIdentity
	// TokenParent is, for a registry token, the stored token it descends
	// from: a registry token it obtains descends from the same one.
	TokenParent string
	// PasswordExpired marks a principal whose correct password has expired.
	// The middleware lets it through only to change its own password.
	PasswordExpired bool
	// Password marks a principal that a user name and password
	// authenticated, which the Access token API refuses while
	// token.allow_basic_auth is off (#385). An empty TokenID cannot stand
	// for it: anonymous and hand-built principals have none either.
	Password bool
	// PasswordRevision fences a long request against a password change
	// without retaining the credential itself: the SHA-256 digest of the
	// stored password hash that the request's password, or the user of its
	// registry token, matched; zero for any other credential. Only access
	// and the kernel read or set it.
	PasswordRevision [32]byte
	// IsTokenAuthenticated marks a principal authenticated by an access
	// token, whose Scope applies even when it is empty. Only access and the
	// kernel read or set it.
	IsTokenAuthenticated bool
	// TokenSubject retains the verified subject for registry-token
	// revalidation, which checks it against the parent token and resolves it
	// to the user again. Only access and the kernel read or set it.
	TokenSubject string
}

// Anonymous is the principal of a request without credentials when
// anonymous access is on.
var Anonymous = Principal{Name: AnonymousUser, Anonymous: true}
