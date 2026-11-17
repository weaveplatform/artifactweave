package pkgroute

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// CheckDownload applies to path the download policy that DownloadItem
// applies, for a protocol that reads a stored file's blob itself, as
// Bower's archive of a version's files does: nil, or the refusal
// DownloadItem would return. The caller checks read permission first. A
// remote repository's own policy is left to the source boundary, which
// checks the read itself.
func (k *Kit) CheckDownload(r *http.Request, path string) error {
	return k.wrap(RequestFrom(r).refuse(path, false, false))
}

// CheckDocument applies the download policy to a metadata document at the
// path Artifactory 7.x stores or caches it at, before a protocol answers
// it, whether artifactweave stores the document there or builds it for the
// request: 7.x reads a stored document through the download path, which
// refuses a blacked-out repository and a path its include and exclude
// patterns reject (RTFACT-13337 shows an npm metadata read refused so). It
// checks a remote repository's own policy at path too, which the source
// boundary checks at the read's permission path unless the read names a
// PolicyPath. The patterns of the files a document lists are not checked:
// the document lists a release whose file they reject (StoredItem).
//
// It also checks a file that a download finds through metadata reads,
// before those reads run: Pub's archive, CocoaPods' source archive,
// Composer's dist, Cargo's crate and Hex's tarball. CheckDownload would
// leave a remote repository's own policy to the reads, which refuse at
// their own paths, while the refusal must name the file (decision 0032).
//
// It returns nil, or the refusal, which Error answers 404 with the item
// routes' message and IsRefusal recognises; a member request's refusal is
// the member passed over, as DownloadItem's is.
func (k *Kit) CheckDocument(r *http.Request, path string) error {
	return k.wrap(RequestFrom(r).refuse(path, false, true))
}

// CheckBlackedOut refuses a read of a blacked-out repository before a
// protocol computes its answer, for resolution metadata, a document or
// listing a client reads to resolve and install, whose 7.x storage path is
// not documented, so that only the repository's blackout and not its
// patterns can refuse it: 7.x ignores a blacked-out repository and serves
// no download from it, and its file list answers 404 (decision 0032). A
// native search keeps its answer: 6.20's searchers check permissions and
// patterns, not the blackout. path is the one the refusal names, the
// request's own. A virtual repository has no blackout, and a member
// request's refusal is the member passed over.
func (k *Kit) CheckBlackedOut(r *http.Request, path string) error {
	return k.wrap(RequestFrom(r).refuse(path, true, true))
}

func (k *Kit) wrap(err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", k.p.Name, err)
	}
	return nil
}

// MergeMembers returns the requests a metadata document at path is built
// from. For a repository that is not virtual it is the request itself,
// unless CheckDocument refuses path. For a virtual repository, its own
// include and exclude patterns rejecting path refuse the document;
// otherwise there is a member request for each step of the package
// handlers' walk (members), in 7.x's effective order. An empty path checks
// blackout alone, for a document whose 7.x storage path is not documented,
// after the caller's CheckBlackedOut.
func (k *Kit) MergeMembers(r *http.Request, path string) ([]*http.Request, error) {
	if err := k.CheckDocument(r, path); err != nil {
		return nil, err
	}
	if RequestFrom(r).Repo.Class != repo.Virtual {
		return []*http.Request{r}, nil
	}
	_, out, err := k.members(r, path)
	return out, err
}

// DownloadMembers returns the package walk's requests for a file at path.
// It applies virtual and hosted members' patterns at path, including nested
// virtual patterns, but leaves a remote member's own policy to its source
// read. A format may map the file's canonical URL to a different upstream
// PolicyPath, as PyPI does. Remote requests retain the walk's cache-only
// restriction; callers must read them through the source boundary.
func (k *Kit) DownloadMembers(r *http.Request, path string) ([]*http.Request, error) {
	if err := k.CheckDownload(r, path); err != nil {
		return nil, err
	}
	if RequestFrom(r).Repo.Class != repo.Virtual {
		return []*http.Request{r}, nil
	}
	_, _, out, err := k.walkPolicy(r, path, false)
	return out, err
}

// Collect reads the document at path from each request MergeMembers
// returns, in order, with read, and returns what read for a format's
// virtual.Merger, each part with the real member it came from. Through a
// virtual repository a member whose read PassedOver reports is left out
// (virtual.Collect), so the format answers its own not-found when no part
// is left (decision 0008, R3). For a repository that is not virtual the one
// part is the request's own, and read's error is returned as it is, so a
// direct request keeps its own refusal and miss.
func Collect[T any](
	k *Kit,
	r *http.Request,
	path string,
	read func(*http.Request) (T, error),
) ([]virtual.Part[T], error) {
	if err := k.CheckDocument(r, path); err != nil {
		return nil, err
	}
	q := RequestFrom(r)
	if q.Repo.Class != repo.Virtual {
		doc, err := read(r)
		if err != nil {
			return nil, err
		}
		return []virtual.Part[T]{{Member: q.Repo, Doc: doc}}, nil
	}
	members, requests, err := k.members(r, path)
	if err != nil {
		return nil, err
	}
	// The package walk asks each real member once (virtual.Package), so a
	// member's key names its one request.
	byKey := make(map[string]*http.Request, len(members))
	for i, m := range members {
		byKey[m.Repo.Key] = requests[i]
	}
	parts, err := virtual.Collect(r.Context(), members,
		func(_ context.Context, m virtual.Member) (T, error) {
			doc, err := read(byKey[m.Repo.Key])
			if err != nil && PassedOver(err) {
				return doc, fmt.Errorf("%w: %w", virtual.ErrPassOver, err)
			}
			return doc, err
		})
	if err != nil {
		return nil, fmt.Errorf("%s: merge %s: %w", k.p.Name, q.Repo.Key, err)
	}
	return parts, nil
}

// members is the package handlers' walk through the virtual repository of
// r's request for the document at path: the steps virtual.Members gives in
// virtual.Package order, each with its member request. Nested virtual
// repositories are flattened with one visited set, so a cycle or a diamond
// is skipped silently, and a nested one whose patterns reject path is
// dropped (R2); 7.161.26's PackageRepoServiceImpl.getSubRepositories
// (318-339) hands package handlers this list. Each member then passes over
// silently, as 7.x's walk ignores every member that does not serve the
// request (R3):
//   - one of another package type, which only data saved before the save
//     checks can hold (R11, repos.CheckMembers), and one of a class the
//     protocol has no Source for;
//   - one whose download policy refuses path: blacked out, or its patterns
//     reject it (generic.CheckDownload; R5);
//   - one the caller may read nothing in. A package handler counts an
//     unreadable member as not found and never answers 403 for it
//     (RTDEV-88743, RTDEV-81046, RTFACT-30175), unlike the item routes'
//     walk, which keeps a member's Read refusal for its answer. Read is
//     asked at the member's root: a format reads the rows of a merged
//     document from below other folders than the document's own (Pub's
//     .pub/<name>.json lists <name>/), and filters each row by the
//     caller's Read on it, so a narrower folder would drop rows the member
//     serves the caller directly (R10).
//
// A remote member's request is cache-only when the walk takes no upstream
// step (Options.NoUpstream, R7), so its source serves only what its cache
// holds.
func (k *Kit) members(r *http.Request, path string) ([]virtual.Member, []*http.Request, error) {
	_, members, requests, err := k.walk(r, path)
	return members, requests, err
}

// walk is members with the steps virtual.Members gave before any was
// passed over, which the merged metadata cache keys on (Merged).
func (k *Kit) walk(
	r *http.Request,
	path string,
) ([]virtual.Member, []virtual.Member, []*http.Request, error) {
	return k.walkPolicy(r, path, true)
}

func (k *Kit) walkPolicy(
	r *http.Request,
	path string,
	remotePolicy bool,
) ([]virtual.Member, []virtual.Member, []*http.Request, error) {
	v := RequestFrom(r).Repo
	o := virtual.Options{Order: virtual.Package, Path: path, NoPriority: k.p.NoPriority}
	if k.o.NoUpstream != nil {
		o.NoUpstream = k.o.NoUpstream(r, v)
	}
	steps, err := virtual.Members(r.Context(), k.o.Store.Repos(), v, o)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", k.p.Name, err)
	}
	members := make([]virtual.Member, 0, len(steps))
	requests := make([]*http.Request, 0, len(steps))
	for _, m := range steps {
		if src, ok := k.walkSource(r.Context(), v, m.Repo, path, "", remotePolicy); ok {
			members = append(members, m)
			requests = append(requests, memberRequest(r, v, m, src))
		}
	}
	return steps, members, requests, nil
}

// walkSource is the Source the package handlers' walk through the virtual
// repository v reads its member m with, or false when the walk passes m
// over silently (members): m is of a type v does not take or of a class
// with no Source, its download policy refuses path, or the caller may read
// nothing at or below readAt in it.
func (k *Kit) walkSource(ctx context.Context, v, m repo.Config, path, readAt string, remotePolicy bool) (Source, bool) {
	src, ok := k.p.Sources[m.Class]
	// A federated member is hosted for virtual resolution (R1), even
	// when the protocol does not serve that class directly, as Hex does not.
	if !ok && m.Class == repo.Federated {
		src, ok = k.p.Sources[repo.Local]
	}
	policyPath := path
	if m.Class == repo.Remote && !remotePolicy {
		policyPath = ""
	}
	if !ok || !repo.AcceptsMember(v.PackageType, m.PackageType) ||
		generic.CheckDownload(m, policyPath, false) != nil || !readable(ctx, m, readAt, true) {
		return nil, false
	}
	return src, true
}

// PassedOver reports whether err, from a read of one member of a virtual
// repository, passes over that member as 7.x's walk passes over every
// member that does not serve the request (decision 0008, R3), so the
// format's merge leaves it out and its single-file read tries the next:
//   - a miss (kernel.ErrNotFound, or a 404 the kit or the source boundary
//     answers), an offline remote included, whose cache misses;
//   - the download policy's refusal, the kit's or the source boundary's,
//     a blacked-out remote included (IsRefusal);
//   - a Read refusal of the member, which a package handler counts as not
//     found (RTDEV-88743, RTDEV-81046, RTFACT-30175);
//   - an upstream's 5xx or a failure to reach it. 7.x's
//     processStandard ignores every member resource it did not find, and
//     answers 403 only for a Read refusal, so a remote that fails passes
//     over too [I].
//
// A fault of artifactweave's own storage (format.ErrStorageFault) never
// passes over: a merge that dropped it would answer short without a sign.
func PassedOver(err error) bool {
	var answer *apierror.Error
	var upstream *source.HTTPError
	switch {
	case err == nil, errors.Is(err, format.ErrStorageFault):
		return false
	case errors.Is(err, kernel.ErrNotFound), IsRefusal(err), errors.Is(err, source.ErrDenied),
		errors.Is(err, kernel.ErrForbidden), errors.Is(err, kernel.ErrUnauthenticated),
		errors.Is(err, source.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.As(err, &upstream):
		return upstream.Status >= http.StatusInternalServerError
	case errors.As(err, &answer):
		// SourceError's answers: a miss, an unreachable or timed-out
		// upstream, and an upstream's own 5xx, which it relays without the
		// challenge.
		return answer.Status == http.StatusNotFound || answer.Status == http.StatusBadGateway ||
			answer.Status == http.StatusGatewayTimeout ||
			answer.NoChallenge && answer.Status >= http.StatusInternalServerError
	}
	return false
}

// IsRefusal reports whether err is the download policy's refusal of the path
// a lookup, read or check named, the kit's or the source boundary's, for a
// protocol that answers its own "not found" for a missing item but must
// pass the policy's 404 and message on unchanged for a refused one: the
// kit's refusal is kernel.ErrNotFound too, so a protocol that tests only
// that turns it into its own answer. A member's refusal under a virtual
// repository is not one: the virtual repository passes over that member,
// and its answer says nothing of how the member is set.
func IsRefusal(err error) bool { return errors.As(err, new(*format.RejectionError)) }

// download refuses what Artifactory refuses before it looks for the file a
// download names (refuse): any path of a blacked-out repository, and a path
// the repository's include and exclude patterns reject.
func (q Request) download(path string) error { return q.refuse(path, false, false) }

// refuse refuses what Artifactory refuses before it looks for what a read
// names, whether or not it exists and for an administrator too
// (generic.CheckDownload, decision 0032): any path of a blacked-out
// repository, and unless blackoutOnly a path the repository's include and
// exclude patterns reject. A member request checks the virtual repository
// it came through first, whose patterns rejecting the path leave it no
// member to search; a virtual repository has no blackout. A member that
// refuses is then passed over as one without the file is, so the answer is
// the virtual repository's "not found", which says nothing of how the
// member is set. A remote repository's own policy is checked here only
// when remote is set: otherwise the source boundary checks its reads, and
// Remote has no Item.
func (q Request) refuse(path string, blackoutOnly, remote bool) error {
	if q.virtual != nil && !blackoutOnly {
		if err := refused(generic.CheckDownload(*q.virtual, path, false)); err != nil {
			return err
		}
	}
	var err error
	switch {
	case q.Repo.Class == repo.Remote && !remote:
	case blackoutOnly:
		if q.Repo.Class != repo.Virtual {
			err = generic.CheckBlackedOut(q.Repo, path)
		}
	default:
		err = generic.CheckDownload(q.Repo, path, false)
	}
	if err != nil && q.virtual != nil {
		return passOver(q.Repo.Key, path)
	}
	return refused(err)
}

// passOver is a member's refusal under a virtual repository: both
// kernel.ErrNotFound and the virtual repository's 404, so a search passes
// over the member and a handler that hands it to Error answers the 404
// rather than a fault.
func passOver(key, path string) error {
	return fmt.Errorf("download %s:%s: %w: %w", key, path, kernel.ErrNotFound, notFound())
}

// refusedError is a download the policy refuses. Protocols treat it as
// kernel.ErrNotFound, so a virtual repository's search passes over the
// refusing member. Error finds its 404 as an *apierror.Error, and
// SourceError as the *format.RejectionError, so both answer with
// generic.CheckDownload's message.
type refusedError struct {
	rejection *format.RejectionError
	answer    *apierror.Error
}

// refused wraps CheckDownload's refusal; nil stays nil.
func refused(err error) error {
	var rejection *format.RejectionError
	if !errors.As(err, &rejection) {
		return err
	}
	return &refusedError{
		rejection: rejection,
		answer:    apierror.New(rejection.Status, rejection.Message),
	}
}

func (e *refusedError) Error() string { return e.rejection.Message }

func (e *refusedError) Unwrap() []error {
	return []error{e.answer, e.rejection, kernel.ErrNotFound}
}
