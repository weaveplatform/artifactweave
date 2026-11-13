package api

import (
	"strings"
	"unicode"
	"unicode/utf16"
)

// bearerPrefix is the Bearer scheme as 7.x reads it, in any case and with
// one space after it.
const bearerPrefix = "bearer "

// propsBearerPrefix and propsBearerMinLength are the Bearer scheme as
// 7.x's props filter reads it: "Bearer " exactly, in a value longer than
// eight UTF-16 units (AuthenticationFilterUtils.getOauthTokenKeyValue with
// old.check.for.oath.token.length at its default, true).
const (
	propsBearerPrefix    = "Bearer "
	propsBearerMinLength = len(propsBearerPrefix) + 2
)

// BearerToken returns the token of an Authorization value that 7.x reads
// as a Bearer credential, reporting false for any other value. 7.161.26
// reads one in two places: the props filter takes "Bearer " exactly in a
// value of at least nine UTF-16 units, whatever follows, and
// RequestUtils.getBearerAuthenticationValue takes "bearer " in any case
// followed by a value that is not blank. So "Bearer" alone, "Bearer " with
// at most one space after it, a lower-case "bearer" with only spaces after
// it, and "Bearer" followed by a tab carry no Bearer credential. The token
// comes back without the spaces around it, as artifactweave has always
// verified it; a value of spaces alone is read and does not verify.
func BearerToken(value string) (string, bool) {
	if len(value) < len(bearerPrefix) ||
		!strings.EqualFold(value[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	rest := value[len(bearerPrefix):]
	props := strings.HasPrefix(value, propsBearerPrefix) &&
		len(utf16.Encode([]rune(value))) >= propsBearerMinLength
	if !props && Blank(rest) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Blank reports whether s is empty or holds only whitespace as Java's
// Character.isWhitespace defines it, the test of Commons Lang's
// StringUtils.isBlank, which 7.x applies to the value after the Basic and
// Bearer schemes and to the X-JFrog-Art-Api header: the space separators
// but the no-break spaces, the line and paragraph separators, and the
// ASCII controls tab to carriage return and file to unit separator.
func Blank(s string) bool {
	for _, r := range s {
		switch {
		case r == ' ', r == ' ', r == ' ':
			return false
		case '\t' <= r && r <= '\r', '\u001c' <= r && r <= '\u001f':
		case unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp):
		default:
			return false
		}
	}
	return true
}
