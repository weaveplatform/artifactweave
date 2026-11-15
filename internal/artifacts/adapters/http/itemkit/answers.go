package itemkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// memberReadKey marks a context MemberRead returns.
type memberReadKey struct{}

// MemberRead returns ctx for a virtual repository's read of a file through
// GetItem under a member's key, as a P2 virtual repository reads its
// members' files. GetItem then answers a miss, a refusal and a matrix
// mismatch as a virtual repository does (fileRequest).
func MemberRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, memberReadKey{}, true)
}

// fileRequest reports whether req asks c's item route for a file, which
// 7.161.26 answers through DownloadServiceImpl.respondResourceNotFound when
// it misses or is refused (#1572). A request ending in a slash is 7.x's
// directory listing (RepoFilter.doDirectoryRequest), and a virtual
// repository's answers, a member read through it included, are P10's
// (#1533), so they keep ItemNotFound and httpkit.Denied.
func fileRequest(c repo.Config, req ItemRequest) bool {
	return req.Path != "" && !req.IsFolder && !req.member && c.Class != repo.Virtual
}

// resourcePath is the path 7.x's download checks and looks up for p: an
// archive entry's archive (ArtifactoryRequestBase.zipResourcePath) and a
// checksum's file (DownloadRequestContext.getResourcePath), so both answers
// name the file and not the path the client asked for.
func resourcePath(p string) string {
	if archive, _, ok := strings.Cut(p, "!/"); ok {
		return archive
	}
	if artifact, _, ok := item.Sidecar(p); ok {
		return artifact
	}
	return p
}

// pathSuffix is what respondResourceNotFound appends to the reason of every
// download it cannot serve. key is the repository the request named, a
// remote's "-cache" key included.
func pathSuffix(key, p string) string { return "; Path: '" + key + ":" + p + "'" }

// missed answers req, a request c does not hold. A file request takes the
// reason 7.161.26's repository gives: DbStoringRepoMixin.getInfo's "File
// not found." for a local, federated or cache repository; for a remote,
// RemoteRepoBase.internalGetInfo's offline text when it is offline, and
// otherwise HttpRepo.returnUnfoundResource's, the reason phrase of the
// upstream's status line, which is http.StatusText's for a 404 [I: an
// upstream may send another phrase, or none over HTTP/2]. cached is whether
// the request named the remote's cache. Other requests keep ItemNotFound.
func missed(c repo.Config, cached bool, req ItemRequest) error {
	if !fileRequest(c, req) {
		return ItemNotFound()
	}
	p := resourcePath(req.Path)
	reason := "File not found."
	if c.Class == repo.Remote && !cached {
		reason = http.StatusText(http.StatusNotFound)
		if c.Offline != nil && *c.Offline {
			// RepoBase.toString is the key.
			reason = fmt.Sprintf(
				"%s: is offline, '%s:%s' is not found at '%s'.",
				c.Key,
				c.Key,
				p,
				p,
			)
		}
	}
	return apierror.New(http.StatusNotFound, reason+pathSuffix(req.Key, p))
}

// mismatched answers req, whose file does not carry a mandatory matrix
// parameter req names. A file request takes DbStoringRepoMixin.getInfo's
// PROPERTY_MISMATCH reason, 404 "File '<repoPath>' was found, but mandatory
// properties do not match.", and the path suffix, both for the repository
// that holds the file: on a remote its cache, whose answer
// RemoteRepoBase.internalGetInfo returns for the cached copy (7.161.26).
// Other requests keep ItemNotFound.
func mismatched(c repo.Config, req ItemRequest) error {
	if !fileRequest(c, req) {
		return ItemNotFound()
	}
	key := c.Key
	if c.Class == repo.Remote {
		key += model.CacheSuffix
	}
	p := resourcePath(req.Path)
	return apierror.New(http.StatusNotFound, "File '"+key+":"+p+
		"' was found, but mandatory properties do not match."+pathSuffix(key, p))
}

// refused answers req, a request caller may not read. A file request takes
// 7.161.26's answer: RealRepoBase.assertReadPermissions' reason, for the
// path checkDownloadIsAllowed checks, which on a remote is its cache's
// ("permissions are always on the cache repo key", RemoteRepoBase), with
// the path suffix. An anonymous caller is answered by
// HttpArtifactoryResponse.sendAuthorizationRequired instead
// (shouldSendAuthRequired), which writes 401 "Unauthorized" in place of the
// reason, and apierror adds its Basic challenge. Other requests keep
// httpkit.Denied.
func refused(c repo.Config, req ItemRequest, caller kernel.Principal) error {
	if !fileRequest(c, req) {
		return httpkit.Denied(caller)
	}
	if caller.Anonymous {
		return apierror.New(http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}
	p := resourcePath(req.Path)
	checked := req.Key
	if c.Class == repo.Remote {
		checked = c.Key + model.CacheSuffix
	}
	return apierror.New(http.StatusForbidden, fmt.Sprintf(
		"Download request for repo:path '%s:%s' is forbidden for user: '%s'.",
		checked,
		p,
		caller.Name,
	)+pathSuffix(req.Key, p))
}

// checksumRefused answers a checksum request for a file the caller may not
// read. 7.161.26 checks Read on the file the checksum names
// (DownloadRequestContext.getResourcePath), so a file request is refused as
// the file is; any other request keeps the 404 that hides the file.
func checksumRefused(c repo.Config, req ItemRequest, caller kernel.Principal) error {
	if !fileRequest(c, req) {
		return ItemNotFound()
	}
	return refused(c, req, caller)
}

// sourceAnswer answers err, a remote source's failure to read req, as
// pkgroute.SourceError does, except that a file request's miss and want of
// Read take the download's answers (missed, refused). The failures
// SourceError tests before those two keep its answers: a refusal of the
// download policy can be kernel.ErrNotFound too (pkgroute.IsRefusal).
func sourceAnswer(c repo.Config, req ItemRequest, caller kernel.Principal, err error) error {
	if fileRequest(c, req) {
		var failure *source.HTTPError
		switch {
		case errors.Is(err, format.ErrStorageFault),
			pkgroute.IsRefusal(err),
			errors.As(err, &failure),
			errors.Is(err, source.ErrInvalidRequest),
			errors.Is(err, source.ErrUnsupported):
			// SourceError's answer, below.
		case errors.Is(err, source.ErrDenied):
			return refused(c, req, caller)
		case errors.Is(err, kernel.ErrNotFound):
			return missed(c, strings.HasSuffix(req.Key, model.CacheSuffix), req)
		}
	}
	//nolint:wrapcheck // typed HTTP response
	return pkgroute.SourceError(err, caller.Anonymous)
}
