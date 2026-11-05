package api

import (
	"errors"
	"regexp"
	"strings"
)

// ErrNotRevocable refuses to revoke a token shorter than the revocable
// threshold that was neither forced revocable nor refreshable.
var ErrNotRevocable = errors.New("token: token is not revocable")

// referenceTokenPattern is the documented reference-token form: base64 of
// "reftkn:01..." ("cmVmd" plus 59 alphanumerics, 64 characters in all).
var referenceTokenPattern = regexp.MustCompile(`^cmVmd[A-Za-z0-9]{59}$`)

// IsReferenceToken reports whether s has the reference-token form, which is
// how the authenticator tells a reference token from a JWT or an API key.
func IsReferenceToken(s string) bool { return referenceTokenPattern.MatchString(s) }

// LooksLikeToken reports whether a Basic password is an access or reference
// token rather than a password: the JFrog CLI sends tokens as the password
// of a Basic credential.
func LooksLikeToken(s string) bool {
	return IsReferenceToken(s) ||
		(strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2)
}
