package itemkit

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// errForced answers a request without credentials to a repository that
// forces authentication (Forced). apierror.Write gives a 401 of the item
// routes' Artifactory profile the Basic challenge, Basic realm="Artifactory
// Realm". The message is the one 7.x's force-authentication filter sends
// (OssForceAuthenticationFilter.sendHttpAuthChallenge, 7.161.26, which calls
// HttpUtils.sendErrorResponse(response, 401, "Unauthorized"); [J]
// RTFACT-30189 shows it from 7.71.8), so it is not kernel.ErrUnauthenticated's
// "Authentication is required", which a request without credentials gets
// elsewhere while anonymous access is off.
var errForced = apierror.New(http.StatusUnauthorized, "Unauthorized")

// Forced reports whether c asks a client for credentials before it is
// used: a Maven virtual repository with forceMavenAuthentication on, or a
// P2 virtual repository with forceP2Authentication on. These are the two
// cases 7.x's filter checks (OssForceAuthenticationFilter.
// isMavenVirtualRepoForcedAuthentication, 7.161.26), on the descriptor's
// own package type. So a Gradle, Ivy or SBT virtual repository stores
// forceMavenAuthentication with no effect [I]: JFrog's settings say the
// field is "relevant for Maven virtual repositories only" ([D]
// virtual-repositories), and [J] RTFACT-29829, which asked for it on
// Gradle, was closed undone. A local, remote or federated repository's
// stored flag changes nothing either: JFrog offers Force Authentication for
// both types on virtual repositories alone. NuGet's and Conan's flags are
// their protocols' own, under /artifactory/api/.
func Forced(c repo.Config) bool {
	if c.Class != repo.Virtual {
		return false
	}
	switch c.PackageType {
	case repo.Maven:
		return isTrue(c.ForceMavenAuthentication)
	case repo.P2:
		return isTrue(c.ForceP2Authentication)
	}
	return false
}

func isTrue(b *bool) bool { return b != nil && *b }

// ForceAuthentication is the middleware that answers errForced to a
// request without credentials (withoutCredentials) whose path,
// /artifactory/<key>[/...], names a repository that Forced reports, for
// every method and query, ?trace included, before the request is routed or
// anything in the repository is read. The client sends credentials only
// after a 401: an anonymous Maven or Eclipse client that got the anonymous
// user's view would never send them ([D] maven-repositories, "Force
// Authentication on Virtual Maven Repositories": Artifactory "will request
// authentication parameters from the Maven client before trying to access
// this repository"; [D] p2-repositories; [J] RTFACT-29198).
//
// It runs ahead of authentication, where 7.x's filter sits in its
// authentication chain, so the answer is the same whether anonymous access
// is on or off. Like that filter it reads the key from the request's path
// (HttpArtifactoryRequest.getRepoKey), so only the flag of the repository
// the request names counts: a nested virtual repository read through
// another one is not asked [I]. Requests under /artifactory/api/, /ui/ and
// /webapp/, which the filter leaves alone, and every request whose
// credential names a user other than anonymous pass untouched, without a
// store read. A store that cannot tell whether the repository is forced,
// or an anonymous access setting that cannot be read, answers 500 rather
// than serve it.
func (s *Kit) ForceAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := pathRepoKey(r.URL.EscapedPath())
		cred := presented(r)
		if !ok || cred == namedCredential || s.Store == nil {
			next.ServeHTTP(w, r)
			return
		}
		// A remote repository's cache key never names a virtual repository.
		if _, cached := repo.ResolveKey(key); cached {
			next.ServeHTTP(w, r)
			return
		}
		c, err := s.Store.Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) || (err == nil && !Forced(c)) {
			next.ServeHTTP(w, r)
			return
		}
		none := false
		if err == nil {
			none, err = s.withoutCredentials(r, cred)
		}
		switch {
		case err != nil:
			httpkit.WriteErr(w, r, s.InternalError(r.Context(), "force authentication", err))
		case none:
			httpkit.WriteErr(w, r, errForced)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// credential is the kind of credential a request presents, as far as the
// force-authentication check needs to know it.
type credential int

const (
	// noCredential: none of the credentials 7.x's filter looks for.
	noCredential credential = iota
	// anonymousBasic: Basic credentials whose user name is blank or
	// anonymous.
	anonymousBasic
	// bearer: a Bearer token, whose subject only verifying it tells.
	bearer
	// namedCredential: any other credential, valid or not.
	namedCredential
)

// presented classes the credential r carries as 7.x's filter does
// (OssForceAuthenticationFilter.acceptFilter and
// checkIfAuthenticationShouldBeIgnored, 7.161.26). A request carries one
// when it has an Authorization header, whatever its value, empty included
// (AuthenticationFilterUtils.isAuthHeaderPresent); a non-blank
// X-JFrog-Art-Api, or else an X-Api-Key header (getApiKeyTokenKeyValue); or
// a non-blank RT_IDN_TOKEN cookie (getTokenKeyValueFromCookies). The SSO
// header, a UI session and a client certificate, which the filter also
// counts, are credentials artifactweave does not read. The filter then
// tells apart two forms of Authorization only, each spelled as
// RequestUtils spells it: "Basic " exactly, followed by a non-blank value
// other than "Og==" (getBasicAuthorization), whose user name it decodes
// (basicUser); and "bearer " in any case followed by a non-blank token
// (getBearerAuthenticationValue). Every other value, such as "Basic Og==",
// a lowercase "basic" or an empty header, is a credential the filter
// passes on, so the authentication answers it as it would without the
// flag. NuGet's X-NuGet-ApiKey counts only under /artifactory/api/nuget/,
// which pathRepoKey never reports.
func presented(r *http.Request) credential {
	if _, ok := r.Header["Authorization"]; !ok {
		if otherCredential(r) {
			return namedCredential
		}
		return noCredential
	}
	h := r.Header.Get("Authorization")
	if rest, ok := strings.CutPrefix(h, "Basic "); ok && !blank(rest) && rest != "Og==" {
		if user := basicUser(rest); blank(user) || strings.ToLower(user) == kernel.AnonymousUser {
			return anonymousBasic
		}
		return namedCredential
	}
	if bearerToken(h) != "" {
		return bearer
	}
	return namedCredential
}

// otherCredential reports whether r carries one of the credentials 7.x's
// filter counts besides the Authorization header: an API key header, as
// AuthenticationFilterUtils.getApiKeyTokenKeyValue reads them, or the
// RT_IDN_TOKEN cookie with a non-blank value.
func otherCredential(r *http.Request) bool {
	if !blank(r.Header.Get("X-JFrog-Art-Api")) {
		return true
	}
	if _, ok := r.Header["X-Api-Key"]; ok {
		return true
	}
	for _, c := range r.Cookies() {
		if c.Name == "RT_IDN_TOKEN" && !blank(c.Value) {
			return true
		}
	}
	return false
}

// bearerToken returns the token of an Authorization value h that 7.x's
// RequestUtils.getBearerAuthenticationValue reads as Bearer: "bearer " in
// any case and then a non-blank token, which it hands to verification as
// it is. Any other value returns "".
func bearerToken(h string) string {
	const prefix = "bearer "
	if len(h) <= len(prefix) || strings.ToLower(h[:len(prefix)]) != prefix ||
		blank(h[len(prefix):]) {
		return ""
	}
	return h[len(prefix):]
}

// basicUser returns the user name in the value of a Basic credential as
// 7.x's RequestUtils.getUsernameOrPasswordFromToken reads it: decoded as
// Apache Commons Codec's Base64.decodeBase64 does, which skips characters
// outside both base64 alphabets, stops at the first '=' and drops the bits
// left over, then cut at the first ':'. A value without ':' has a blank user
// name.
func basicUser(value string) string {
	value, _, _ = strings.Cut(value, "=")
	clean := make([]byte, 0, len(value))
	for i := range len(value) {
		switch c := value[i]; {
		case c == '-':
			clean = append(clean, '+')
		case c == '_':
			clean = append(clean, '/')
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '+', c == '/':
			clean = append(clean, c)
		}
	}
	if len(clean)%4 == 1 {
		clean = clean[:len(clean)-1]
	}
	// clean holds characters of the alphabet alone, in a length that
	// decodes, so Decode has no error to report.
	decoded := make([]byte, base64.RawStdEncoding.DecodedLen(len(clean)))
	n, _ := base64.RawStdEncoding.Decode(decoded, clean)
	user, _, ok := strings.Cut(string(decoded[:n]), ":")
	if !ok {
		return ""
	}
	return user
}

// blank reports whether s is empty or holds only whitespace as Java's
// Character.isWhitespace defines it, the test of Commons Lang's
// StringUtils.isBlank, which 7.x's filter uses: the space separators but
// the no-break spaces, the line and paragraph separators, and the ASCII
// controls tab to carriage return and file to unit separator.
func blank(s string) bool {
	for _, r := range s {
		switch {
		case r == '\u00a0', r == '\u2007', r == '\u202f':
			return false
		case '\t' <= r && r <= '\r', '\u001c' <= r && r <= '\u001f':
		case unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp):
		default:
			return false
		}
	}
	return true
}

// withoutCredentials reports whether a request whose credential is cred
// counts as carrying none. While anonymous access is on, 7.x's filter
// counts Basic credentials of a blank or anonymous user name, and a Bearer
// token that does not verify or whose subject is anonymous, as none, so
// that they do not reach the repository as the anonymous user
// (OssForceAuthenticationFilter.checkIfAuthenticationShouldBeIgnored and
// verifyBearerAuthPresent, 7.161.26). A Bearer token whose check fails for
// another reason, such as a store error or a restricted user, is left to
// the authentication to answer. While anonymous access is off, any
// credential counts, and the authentication answers it.
func (s *Kit) withoutCredentials(r *http.Request, cred credential) (bool, error) {
	auth := s.Access.Authenticator
	if cred == noCredential || auth == nil {
		return cred == noCredential, nil
	}
	ctx := r.Context()
	on, err := auth.AnonymousAllowed(ctx)
	if err != nil {
		return false, fmt.Errorf("anonymous access: %w", err)
	}
	if !on {
		return false, nil
	}
	if cred == anonymousBasic {
		return true, nil
	}
	p, err := auth.Token(ctx, bearerToken(r.Header.Get("Authorization")), "")
	if err != nil {
		return errors.Is(err, kernel.ErrBadCredentials), nil
	}
	return p.Anonymous || strings.EqualFold(p.Name, kernel.AnonymousUser), nil
}

// pathRepoKey returns the repository key an escaped request path names
// under /artifactory/, as 7.x's request parsing finds it
// (ArtifactoryRequestBase.calculateRepoPath, 7.161.26): the first segment,
// without its matrix parameters, decoded. The keys 7.x's filter skips
// (api, ui and webapp) and a segment that does not decode, which 7.x's
// filter also lets through, report false.
func pathRepoKey(escaped string) (string, bool) {
	rest, ok := strings.CutPrefix(escaped, "/artifactory/")
	if !ok {
		return "", false
	}
	segment, _, _ := strings.Cut(rest, "/")
	segment, _, _ = strings.Cut(segment, ";")
	key, err := url.PathUnescape(segment)
	switch {
	case err != nil, key == "", key == "api", key == "ui", key == "webapp":
		return "", false
	}
	return key, true
}
