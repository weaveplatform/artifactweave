package api

import (
	"encoding/base64"
	"strings"
)

// basicPrefix is the one spelling of the Basic scheme 7.x reads, case and
// separator included.
const basicPrefix = "Basic "

// emptyBasic is base64 of ":", an empty user name and password, which some
// clients send when they hold no credentials.
const emptyBasic = "Og=="

// IgnoredAuthorization reports whether an Authorization value names the
// Basic scheme in a form 7.x reads no credential from: the scheme spelled
// other than exactly "Basic " (another case, or a tab after it), a blank
// value, or "Og==". 7.161.26's RequestUtils.getBasicAuthorization admits
// only "Basic " followed by a value neither blank nor "Og==", and no other
// filter of the open source chain reads a Basic value, so AccessFilter
// serves such a request as one without credentials. JFrog meant it so for
// "Og==" since 3.4.0: "Artifactory should ignore it and continue as
// anonymous" (RTFACT-6765). Net/http's Request.BasicAuth matches the scheme
// in any case, which is why callers that must agree with 7.x read Basic
// through BasicCredentials instead. Any other scheme is left to whoever
// reads it.
func IgnoredAuthorization(value string) bool {
	scheme := value
	if i := strings.IndexAny(value, " \t"); i >= 0 {
		scheme = value[:i]
	}
	if !strings.EqualFold(scheme, "Basic") {
		return false
	}
	rest, exact := strings.CutPrefix(value, basicPrefix)
	return !exact || strings.TrimSpace(rest) == "" || rest == emptyBasic
}

// BasicCredentials returns the user name and secret of an Authorization
// value that 7.x reads as Basic credentials. It reports false for another
// scheme, for a value IgnoredAuthorization reports, and for one that does
// not decode to "<user>:<secret>", which the authentication answers 401
// as a credential that does not verify, as Spring's Basic filter does.
func BasicCredentials(value string) (user, secret string, ok bool) {
	rest, exact := strings.CutPrefix(value, basicPrefix)
	if !exact || IgnoredAuthorization(value) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return "", "", false
	}
	user, secret, ok = strings.Cut(string(decoded), ":")
	if !ok {
		return "", "", false
	}
	return user, secret, true
}
