package types

import "regexp"

// referenceTokenPattern is the documented reference-token form: base64 of
// "reftkn:01..." ("cmVmd" plus 59 alphanumerics, 64 characters in all).
var referenceTokenPattern = regexp.MustCompile(`^cmVmd[A-Za-z0-9]{59}$`)

// IsReferenceToken reports whether s has the reference-token form, which is
// how the authenticator tells a reference token from a JWT or an API key.
func IsReferenceToken(s string) bool { return referenceTokenPattern.MatchString(s) }
