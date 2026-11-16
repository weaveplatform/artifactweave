package itemkit

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// remoteMissAnswer answers err, a remote source's read of req, where 7.x's
// download answers it in its own words, and is nil otherwise. A file
// request the source refused to fill the cache for (a
// *source.CacheFillDenied) answers 403 "Rejected artifact download
// request: " and the deploy check's reason, as 7.161.26's
// DownloadServiceImpl.respondFoundResource answers the RepoRejectException
// that RemoteRepoBase.downloadAndSave's deploy check throws (lines
// 1006-1022), with no path suffix, since only respondResourceNotFound adds
// one; an anonymous caller is sent the challenge instead
// (sendAuthorizationRequired, shouldSendAuthRequired), 401 "Unauthorized"
// as refused answers it. A file request on a remote assumed offline
// (source.ErrAssumedOffline) answers 404 with
// RemoteRepoBase.internalGetInfo's reason and the path suffix
// respondResourceNotFound appends, as missed answers a remote that is set
// offline. Any other request leaves both to SourceError, which answers
// them as the misses they also are.
func remoteMissAnswer(c repo.Config, req ItemRequest, caller kernel.Principal, err error) error {
	if !fileRequest(c, req) {
		return nil
	}
	if denied := new(source.CacheFillDenied); errors.As(err, &denied) {
		if caller.Anonymous {
			return apierror.New(http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
		}
		return apierror.New(
			http.StatusForbidden,
			"Rejected artifact download request: "+denied.Message(),
		)
	}
	if errors.Is(err, source.ErrAssumedOffline) {
		p := resourcePath(req.Path)
		// RepoBase.toString is the key.
		return apierror.New(http.StatusNotFound, fmt.Sprintf(
			"%s: is assumed offline, '%s:%s' is not found at '%s'.", c.Key, c.Key, p, p,
		)+pathSuffix(req.Key, p))
	}
	return nil
}
