package p2http

import (
	"context"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// errForced answers an anonymous request to a P2 virtual repository with
// forceP2Authentication on. apierror.Write gives every 401 of the item
// routes' Artifactory profile the Basic challenge, Basic realm="Artifactory
// Realm". The message is the one 7.x's force-authentication filter sends
// (OssForceAuthenticationFilter.sendHttpAuthChallenge, 7.161.26, which calls
// HttpUtils.sendErrorResponse(response, 401, "Unauthorized")), so it is not
// kernel.ErrUnauthenticated's "Authentication is required", which NuGet's and
// Conan's forced routes answer and no 7.x source gives.
var errForced = apierror.New(http.StatusUnauthorized, "Unauthorized")

// viaVirtualKey marks the request a P2 virtual repository makes of a member
// for a file under <member key>/.
type viaVirtualKey struct{}

// throughVirtual returns ctx marked as a P2 virtual repository's read of a
// member.
func throughVirtual(ctx context.Context) context.Context {
	return context.WithValue(ctx, viaVirtualKey{}, true)
}

// forced reports whether an anonymous request to the P2 repository c is
// answered errForced before anything is read, whatever the anonymous user
// may read: the Eclipse client sends credentials only after a 401, so a
// virtual repository that answered an anonymous caller a composite of the
// members it may read would never receive them (RTFACT-29198; [D]
// p2-repositories, "Allow Anonymous Access for P2").
//
// Only a virtual repository's flag counts. JFrog lists Force Authentication
// among the P2 virtual repository settings and not among the P2 remote ones
// ([D] virtual-repositories and remote-repositories), and 7.x's filter reads
// it from a VirtualRepoDescriptor alone (OssForceAuthenticationFilter.
// isMavenVirtualRepoForcedAuthentication, 7.161.26), so a remote's stored
// flag changes no answer. The filter takes the key from the request's path
// (HttpArtifactoryRequest.getRepoKey), so the flag is the one of the
// repository the request names: a nested P2 virtual repository read under
// its outer one's <member key>/ is not asked [I].
func forced(ctx context.Context, c repo.Config) bool {
	if c.Class != repo.Virtual || ctx.Value(viaVirtualKey{}) != nil {
		return false
	}
	return c.ForceP2Authentication != nil && *c.ForceP2Authentication
}
