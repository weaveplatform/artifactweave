package itemkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
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

// Missed is missed for the format overlays (Deps.Overlays), as Refused and
// Mismatched are refused and mismatched: 7.161.26's RepoFilter.doDownload hands
// every GET and HEAD of /artifactory/<key>/<path> to DownloadServiceImpl,
// whatever the package type, and a type's own handling lives inside the
// repository's getInfo, so a file an overlay serves misses and is refused
// as any other file on the item route is (#1599). A virtual repository, its
// member reads and a folder request keep their answers, as there.
func Missed(c repo.Config, cached bool, req ItemRequest) error { return missed(c, cached, req) }

// Refused is refused for the format overlays (Missed).
func Refused(c repo.Config, req ItemRequest, caller kernel.Principal) error {
	return refused(c, req, caller)
}

// Mismatched is mismatched for the format overlays (Missed).
func Mismatched(c repo.Config, req ItemRequest) error { return mismatched(c, req) }

// ErrFolderAtPath is an overlay's read that found a folder where the file
// it serves would be. 7.161.26 answers a file request that finds a folder
// with a redirect to the folder's listing (RemoteRepoBase.getInfo, "Expected
// file but got directory, requesting a redirect"), which no overlay makes,
// so ReadAnswer leaves it to the answer each gave it before #1599. It is
// kernel.ErrNotFound to every other reader, a virtual repository's search
// of its members among them.
var ErrFolderAtPath = fmt.Errorf("itemkit: a folder holds the path: %w", kernel.ErrNotFound)

// ReadAnswer answers err, an overlay's failure to read the file req names,
// as the download answers it, the way sourceAnswer answers the generic
// route's remote read: a want of Read takes refused, and a miss takes
// missed. err may be the source's own error or pkgroute.SourceError's answer
// to it, as the overlays' shared index reads return: a want of Read is
// source.ErrDenied, kernel.ErrForbidden or kernel.ErrUnauthenticated, and a
// miss kernel.ErrNotFound or SourceError's 404 "Not Found" for it. cached is
// whether req named a remote's cache. The failures sourceAnswer keeps are
// returned as they are: a storage fault, the download policy's refusal,
// which can be kernel.ErrNotFound too (pkgroute.IsRefusal), the upstream's
// own status (source.HTTPError, whose SourceError answer sends no
// challenge), an invalid or unsupported request; so are ErrFolderAtPath,
// any other failure, and every failure of a request fileRequest leaves to
// the old answers. The caller answers what is returned as it did before.
func ReadAnswer(
	c repo.Config,
	cached bool,
	req ItemRequest,
	caller kernel.Principal,
	err error,
) error {
	if err == nil || !fileRequest(c, req) {
		return err
	}
	// An overlay path is the item route's, so a cache fill the caller may
	// not make and an assumed-offline miss answer as sourceAnswer answers
	// them, before the miss either one is too.
	if answer := remoteMissAnswer(c, req, caller, err); answer != nil {
		return answer
	}
	var failure *source.HTTPError
	var answer *apierror.Error
	switch {
	case errors.Is(err, format.ErrStorageFault),
		pkgroute.IsRefusal(err),
		errors.As(err, &failure),
		errors.Is(err, source.ErrInvalidRequest),
		errors.Is(err, source.ErrUnsupported),
		errors.Is(err, ErrFolderAtPath):
		return err
	case errors.Is(err, source.ErrDenied),
		errors.Is(err, kernel.ErrForbidden),
		errors.Is(err, kernel.ErrUnauthenticated):
		return refused(c, req, caller)
	case errors.Is(err, kernel.ErrNotFound):
		return missed(c, cached, req)
	case errors.As(err, &answer) && answer.Status == http.StatusNotFound &&
		answer.Message == http.StatusText(http.StatusNotFound) && !answer.NoChallenge:
		return missed(c, cached, req)
	}
	return err
}

// NotServed answers req, a path an overlay does not serve on the item
// route, as the generic route answers a file it does not hold: Read on the
// path first, then the download policy, then the miss, the order of
// 7.161.26's checkDownloadIsAllowed and getInfo. The overlays that keep a
// format's own records from the item route (Chef's api/v1 documents, Conda's
// shards, Hugging Face's Xet records, Machine Learning's staging and
// manifests) and the paths a format refuses answer so: on 7.x such a path
// holds no file the download serves [I for a remote, whose upstream 7.x
// would ask]. A request fileRequest leaves to the old answers keeps
// ItemNotFound whoever asks, as before.
func NotServed(
	c repo.Config,
	cached bool,
	req ItemRequest,
	caller kernel.Principal,
	acc Access,
) error {
	if !fileRequest(c, req) {
		return ItemNotFound()
	}
	p := resourcePath(req.Path)
	if !acc.Folder(c, req.Path, accessapi.ActionRead) || !acc.File(c, p, accessapi.ActionRead) {
		return refused(c, req, caller)
	}
	if err := generic.CheckDownload(c, p, false); err != nil {
		var rejected *format.RejectionError
		if errors.As(err, &rejected) {
			return apierror.New(rejected.Status, rejected.Message)
		}
		return err //nolint:wrapcheck // CheckDownload answers with RejectionError alone
	}
	return missed(c, cached, req)
}

// sourceAnswer answers err, a remote source's failure to read req, as
// pkgroute.SourceError does, except that a file request's miss and want of
// Read take the download's answers (missed, refused). The failures
// SourceError tests before those two keep its answers: a refusal of the
// download policy can be kernel.ErrNotFound too (pkgroute.IsRefusal).
func sourceAnswer(c repo.Config, req ItemRequest, caller kernel.Principal, err error) error {
	if answer := remoteMissAnswer(c, req, caller, err); answer != nil {
		return answer
	}
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
