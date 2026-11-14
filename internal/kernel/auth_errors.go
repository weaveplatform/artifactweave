package kernel

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The messages JFrog clients print. No public source documents Artifactory's
// exact wording; jfrog-client-go shows errors[].message as it comes.
var (
	// ErrBadCredentials answers credentials that do not verify.
	ErrBadCredentials = apierror.New(http.StatusUnauthorized, "Bad credentials")
	// ErrUnauthenticated answers a request that needs a user and has none.
	ErrUnauthenticated = apierror.New(http.StatusUnauthorized, "Authentication is required")
	// ErrForbidden answers an authenticated user without the right.
	ErrForbidden = apierror.New(http.StatusForbidden, "Forbidden")
)

// ErrAPIRestricted answers a user whose API access an administrator has
// temporarily restricted (decision 0043). It is returned only after the
// credential has been verified.
var ErrAPIRestricted = apierror.New(http.StatusForbidden, "API access is temporarily restricted")

// ErrPasswordExpired answers a correct password the expiration policy, or
// an administrator, has expired. It is reported only after the password
// verifies, so it tells nothing to someone without it. 403 rather than 401,
// with Artifactory's message: its entry point answers a
// CredentialsExpiredException as it does a locked account (decision 0005).
var ErrPasswordExpired = apierror.New(http.StatusForbidden, "Password has expired")

// ErrAccountLocked answers any password for a locked account. 403, as
// Artifactory's entry point answers a LockedException, with the message of
// 7.161.26's UserLockedException.userLocked, which AccessFilter's lock check
// throws before any password is read. The text names a wrong password as
// well as the lock "to avoid any clue for the hacker to identify valid user
// accounts" (RTFACT-24156, 7.11), so it replaces OSS 6.20's "User is
// Locked." (decision 0005). It differs from the 403 of access's temporary
// login suspension, which ends by itself, where this lasts until an
// administrator unlocks the account.
var ErrAccountLocked = apierror.New(http.StatusForbidden,
	"Login has failed. Due to Incorrect username/password or locked user.")
