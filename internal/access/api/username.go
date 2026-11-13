package api

import "strings"

// FoldUsername is the spelling a user name is stored and matched in: lower
// case. Access compares user names without regard to case and stores them
// in lower case: JFrog's SCIM page says "User names are case-insensitive",
// Get Permissions matches "Usernames ... case-insensitively, group names
// exactly", and RTFACT-29660 (7.55.13) says that a user created "with
// capitalization" is stored lower-case. Group names keep their case
// (#1309).
//
// The rule is here, not only in the access module's model, so that another
// module that compares a user name a client sent with the request's
// principal, as Conan's login does, matches it as the authenticator did.
// strings.ToLower rather than strings.EqualFold, so that matching is
// equality of the spelling the stores hold.
func FoldUsername(name string) string { return strings.ToLower(name) }

// SameUsername reports whether a and b name the same user.
func SameUsername(a, b string) bool { return FoldUsername(a) == FoldUsername(b) }
