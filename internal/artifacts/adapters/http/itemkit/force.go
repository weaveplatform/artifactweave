package itemkit

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

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
// request without credentials whose path, /artifactory/<key>[/...], names a
// repository that Forced reports, for every method and query, ?trace
// included, before the request is routed or anything in the repository is
// read. The client sends credentials only after a 401: an anonymous Maven
// or Eclipse client that got the anonymous user's view would never send
// them ([D] maven-repositories, "Force Authentication on Virtual Maven
// Repositories": Artifactory "will request authentication parameters from
// the Maven client before trying to access this repository"; [D]
// p2-repositories; [J] RTFACT-29198).
//
// It runs ahead of authentication, where 7.x's filter sits in its
// authentication chain, so the answer is the same whether anonymous access
// is on or off. Like that filter it reads the key from the request's path
// (HttpArtifactoryRequest.getRepoKey), so only the flag of the repository
// the request names counts: a nested virtual repository read through
// another one is not asked [I]. Requests under /artifactory/api/, /ui/ and
// /webapp/, which the filter leaves alone, and every request that carries
// credentials (hasCredentials) pass untouched. A store that cannot tell
// whether the repository is forced answers 500 rather than serve it.
func (s *Kit) ForceAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := pathRepoKey(r.URL.EscapedPath())
		if !ok || hasCredentials(r) || s.Store == nil {
			next.ServeHTTP(w, r)
			return
		}
		// A remote repository's cache key never names a virtual repository.
		if _, cached := repo.ResolveKey(key); cached {
			next.ServeHTTP(w, r)
			return
		}
		c, err := s.Store.Repos().Get(r.Context(), key)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			next.ServeHTTP(w, r)
		case err != nil:
			httpkit.WriteErr(w, r, s.InternalError(r.Context(), "force authentication", err))
		case Forced(c):
			httpkit.WriteErr(w, r, errForced)
		default:
			next.ServeHTTP(w, r)
		}
	})
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

// hasCredentials reports whether r carries a credential, valid or not, in
// one of the forms the authentication middleware reads on an item path
// (decision 0005): an Authorization header, Basic or Bearer, or an
// X-JFrog-Art-Api reference token. NuGet's X-NuGet-ApiKey counts only under
// /artifactory/api/nuget/, which pathRepoKey never reports. 7.x's filter
// tests for a credential's presence too, not its validity
// (AuthenticationFilterUtils.isRequestContainsAuthentication), so a wrong
// password gets the authentication's 401, not this one.
func hasCredentials(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" || r.Header.Get("X-JFrog-Art-Api") != ""
}
