package itemkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// upstreamStep is a remote member's upstream step in a download's walk
// through a virtual repository (lookupMember), once the member's download
// policy and the caller's Read on the file have let it through: what m's
// upstream answers for the walk's path, as a direct read of m answers it.
// It reports exactMatch with the hit, or passedOver, recording a Read
// refusal in res (R3); an error stops the walk.
type upstreamStep func(
	ctx context.Context,
	dl *Download,
	m repo.Config,
	res *virtual.Resolution,
) (Found, memberAnswer, error)

// memberRequest is req as a direct request to the member m names it: the
// same path and matrix parameters under m's key. A remote member reached
// through a virtual repository reads and answers under its own rules
// (decision 0008, R10), so its read is a direct read of m's.
func memberRequest(req ItemRequest, m repo.Config) ItemRequest {
	req.Key = m.Key
	return req
}

// upstreamReads reports whether a walk asks the remote member c's upstream
// for the file p: where a direct read of c on the item route asks it
// (GetItem), a type remoteDownloadType reads through its source and one
// coldReadType reads once the cache lookup has missed, at a path
// coldReadPath admits. CRAN and opkg fetch through their overlays, which
// a walk does not run, so their members are read from their caches alone
// [I].
func (s *Kit) upstreamReads(c repo.Config, p string) bool {
	return remoteDownloadType(c, false) || coldReadType(c, false) && s.coldReadPath(c, p)
}

// walkStep is the upstream step of the walk for req itself, or nil where
// that walk takes none: a checksum or an archive entry, whose walk looks
// for the file it names (getSidecar, getUnfoundItem), as 7.161.26's walk
// looks for the resource DownloadRequestContext.getResourcePath gives.
func (s *Kit) walkStep(r *http.Request, req ItemRequest, p kernel.Principal, acc Access) upstreamStep {
	if resourcePath(req.Path) != req.Path {
		return nil
	}
	return s.fileStep(r, req, p, acc)
}

// fileStep is the upstream step of a walk for req, a file or the checksum
// of one, or nil without a remote source. It reads the member as
// getRemoteItem reads a remote (readRemoteItem), with the member's own
// request, and the walk passes over what it does not find (upstreamMiss).
// JFrog's Virtual Repositories page searches "remote repository caches,
// and finally remote repositories themselves", and of a remote found to
// have the file says Artifactory "will download it and update the remote
// repository cache", so the hit is what a direct read of the member gives,
// a file its cache now holds or a stream when it stores nothing, which the
// route then answers as that read (serveRemoteRead).
func (s *Kit) fileStep(r *http.Request, req ItemRequest, p kernel.Principal, acc Access) upstreamStep {
	if s.RemoteSources == nil {
		return nil
	}
	return func(
		ctx context.Context, dl *Download, m repo.Config, res *virtual.Resolution,
	) (Found, memberAnswer, error) {
		if !s.upstreamReads(m, dl.path) {
			return Found{}, passedOver, nil
		}
		read, ok := s.readRemoteItem(r, m, memberRequest(req, m), p, acc, dl.trace)
		if !ok {
			// The member's cache holds the path, which its cache step
			// answered.
			return Found{}, passedOver, nil
		}
		switch upstreamMiss(read) {
		case readServes:
			res.Found = true
			return Found{Repo: m, remote: &read}, exactMatch, nil
		case readDenies:
			deny(ctx, dl, res, m, dl.path)
		default:
			traceMiss(dl, m, read)
		}
		return Found{}, passedOver, nil
	}
}

// archiveStep is the upstream step of a walk for the archive an archive
// entry request req names, or nil without a remote source. 7.161.26's
// RemoteRepoBase.internalGetInfo downloads the archive into the cache
// before it answers an entry of it (getRemoteZipRepoResource), inside the
// member's getInfo, so the walk passes over a member whose download fails:
// one that may not fill its cache, and one that stores nothing, whose
// cache the download cannot leave the archive in (readRemoteArchive). The
// hit is the archive its cache now holds, which the route opens as any
// archive it finds.
func (s *Kit) archiveStep(
	r *http.Request, req ItemRequest, archive string, p kernel.Principal, acc Access,
) upstreamStep {
	if s.RemoteSources == nil {
		return nil
	}
	return func(
		ctx context.Context, dl *Download, m repo.Config, res *virtual.Resolution,
	) (Found, memberAnswer, error) {
		a, read, ok := s.readRemoteArchive(r, m, memberRequest(req, m), archive, p, acc, dl.trace)
		if !ok {
			return Found{}, passedOver, nil
		}
		switch upstreamMiss(read) {
		case readDenies:
			deny(ctx, dl, res, m, archive)
		case readPasses:
			traceMiss(dl, m, read)
		default:
			switch {
			case read.err == nil:
				res.Found = true
				return a, exactMatch, nil
			case errors.As(read.cause, new(*source.CacheFillDenied)):
				// The eager download swallows its failure, and getInfo
				// answers the archive not found.
				traceMiss(dl, m, read)
			default:
				return Found{}, passedOver, read.err
			}
		}
		return Found{}, passedOver, nil
	}
}

// remoteArchive reads the archive of req, an archive entry request on the
// remote repository c, from its upstream once its cache lookup has not
// found it, as 7.161.26's RemoteRepoBase.internalGetInfo downloads it
// (getRemoteZipRepoResource) before the entry is opened. It returns the
// archive the cache now holds, or the answer in its place, and reports
// read false where c reads nothing upstream for it: a -cache alias, a type
// or a remote that stores nothing (readRemoteArchive), which keep the cache
// lookup's miss. A caller who may not fill the cache gets 7.x's answer for
// the download it swallows (EagerResourcesDownloader.downloadNow), 404
// "Could not download '<entry>' from main zip resource" for the archive.
// Every other failure answers as a direct read of the archive does.
func (s *Kit) remoteArchive(
	r *http.Request,
	c repo.Config,
	cached bool,
	req ItemRequest,
	archive, entry string,
	p kernel.Principal,
	acc Access,
	t *resolutionTrace,
) (a Found, read bool, answer error) {
	if c.Class != repo.Remote || cached || s.RemoteSources == nil {
		return Found{}, false, nil
	}
	a, fetched, ok := s.readRemoteArchive(r, c, req, archive, p, acc, t)
	switch {
	case !ok:
		return Found{}, false, nil
	case fetched.err == nil:
		return a, true, nil
	case errors.As(fetched.cause, new(*source.CacheFillDenied)):
		return Found{}, true, apierror.New(http.StatusNotFound, fmt.Sprintf(
			"Could not download '%s' from main zip resource", entry,
		)+pathSuffix(req.Key, archive))
	}
	return Found{}, true, fetched.err
}

// readRemoteArchive reads archive, the file an archive entry request req
// on the remote repository c names, as getRemoteItem reads a file: a GET
// with none of the request's query, matrix parameters, range, conditions
// or Accept, as 7.161.26's getRemoteZipRepoResource downloads the archive
// with an internal request built from its repository path alone
// (InternalRequestFactory.createInternalRequestDisableRedirect). It
// returns the archive c's cache now holds. It reports false for a type
// whose upstream the item route does not read (upstreamReads) and for a
// remote that stores nothing: 7.x's download keeps no archive there to
// open, and its getRemoteZipRepoResource then fails reading the cache it
// lacks, which answers the entry as not found [I], so nothing is asked.
func (s *Kit) readRemoteArchive(
	r *http.Request,
	c repo.Config,
	req ItemRequest,
	archive string,
	p kernel.Principal,
	acc Access,
	t *resolutionTrace,
) (Found, remoteRead, bool) {
	if !s.upstreamReads(c, archive) ||
		c.StoreArtifactsLocally != nil && !*c.StoreArtifactsLocally {
		return Found{}, remoteRead{}, false
	}
	read, ok := s.readRemoteItem(
		archiveRequest(r, archive), c, ItemRequest{Key: req.Key, Path: archive}, p, acc, t,
	)
	switch {
	case !ok:
		return Found{}, remoteRead{}, false
	case read.err != nil:
		return Found{}, read, true
	case read.result.Stream != nil:
		// A transient resource the cache does not keep has no archive
		// to open.
		_ = read.result.Stream.Body.Close()
		return Found{}, remoteRead{}, false
	}
	if err := remoteDeliveryErr(r.Context(), read.result, p.Anonymous); err != nil {
		return Found{}, failedRead(err, errReadFault), true
	}
	return Found{Repo: c, Item: *read.result.Item, Used: read.result.Used}, read, true
}

// archiveRequest is r as the GET of archive, the file of the archive entry
// r names, under the same repository key, with the caller's context and
// none of r's query, headers or matrix parameters (readRemoteArchive).
func archiveRequest(r *http.Request, archive string) *http.Request {
	escapedKey, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"), "/")
	key, _ := url.PathUnescape(escapedKey)
	// The archive keeps the escaping the client gave it, so the upstream is
	// asked for the bytes it was named by; a form that does not decode to
	// the archive is escaped afresh.
	escaped, _, _ := strings.Cut(rest, "!/")
	segments := strings.Split(escaped, "/")
	for i, seg := range segments {
		segments[i], _, _ = strings.Cut(seg, ";")
	}
	escaped = strings.Join(segments, "/")
	if decoded, err := url.PathUnescape(escaped); err != nil || decoded != archive {
		escaped = (&url.URL{Path: archive}).EscapedPath()
	}
	ar := r.Clone(r.Context())
	ar.Method = http.MethodGet
	ar.Header = http.Header{}
	ar.Body = http.NoBody
	ar.URL = &url.URL{
		Path:    "/artifactory/" + key + "/" + archive,
		RawPath: "/artifactory/" + escapedKey + "/" + escaped,
	}
	ar.RequestURI = ""
	return ar
}

// readMiss is how a walk through a virtual repository takes a remote
// member's upstream read (upstreamMiss).
type readMiss int

const (
	// readServes: the member answers, with its result or with err.
	readServes readMiss = iota
	// readPasses: the walk passes over the member.
	readPasses
	// readDenies: the walk passes over the member, which refused the
	// caller Read (R3's 403).
	readDenies
)

// upstreamMiss is how a walk takes read, a remote member's upstream read.
// 7.161.26's processStandard passes over every member whose getInfo does
// not find the resource and keeps only a 403 (checkIfForbidden): an
// upstream miss, a missed-retrieval hit, an offline or assumed-offline
// remote, a refusal of its download policy or of the format, a label the
// client's Accept refuses (HttpRepo.retrieveInfo's REJECTED resource) and
// a property conflict pass over. So do an upstream's own failures: its 401
// or 403 is the upstream's refusal of this server, not the caller's, and
// a 5xx, a timeout or a broken connection end in the unfound resource
// handleGenericException returns [I]. A caller who may not fill the cache
// is answered by the member: 7.x's walk finds the file from the upstream's
// info, and its download then refuses the fill (decision 0008, R10 [I]).
// So is a failure of this server's own storage or reads, as the walk's
// store failures are, and a read the remote's new configuration
// superseded, whose answer asks the client to retry. A refused fill and a
// storage fault are judged first: the one unwraps to a miss, and the other
// may wrap a timeout.
func upstreamMiss(read remoteRead) readMiss {
	cause := read.cause
	switch {
	case read.err == nil,
		errors.As(cause, new(*source.CacheFillDenied)),
		errors.Is(cause, format.ErrStorageFault):
		return readServes
	case errors.Is(cause, source.ErrDenied):
		return readDenies
	case errors.Is(cause, kernel.ErrNotFound),
		errors.As(cause, new(*format.RejectionError)),
		errors.As(cause, new(*source.HTTPError)),
		errors.Is(cause, source.ErrUnavailable),
		errors.Is(cause, source.ErrInvalidResult),
		errors.Is(cause, source.ErrInvalidRequest),
		errors.Is(cause, source.ErrUnsupported),
		errors.Is(cause, context.DeadlineExceeded),
		errors.Is(cause, context.Canceled):
		return readPasses
	}
	return readServes
}

// traceMiss records why a walk passed over the remote member m's upstream
// step, in the words 7.161.26's remote logs: a refusal as
// createUnfoundRepoResource logs it, a miss as the stored lookup's, and a
// failure as RemoteRepoBase.handleGenericException's.
func traceMiss(dl *Download, m repo.Config, read remoteRead) {
	var rejected *format.RejectionError
	switch {
	case errors.As(read.cause, &rejected):
		dl.stepf("Download denied (%s) - returning un-found resource", rejected.Message)
	case errors.Is(read.cause, kernel.ErrNotFound):
		dl.stepf("Unable to find resource in %s", traceRepoPath(m, false, dl.path))
	default:
		dl.stepf("Failed to retrieve information: %s", read.err)
	}
}
