package pkgroute

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
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
// otherwise there is a member request (MemberRequest) for each member in
// the virtual repository's order, leaving out a member that no longer
// exists, one that is blacked out, which 7.x ignores when it resolves, and
// one whose include and exclude patterns reject path, remote members
// included, so no member that would refuse the document adds to it. An
// empty path checks blackout alone, for a document whose 7.x storage path
// is not documented, after the caller's CheckBlackedOut.
func (k *Kit) MergeMembers(r *http.Request, path string) ([]*http.Request, error) {
	if err := k.CheckDocument(r, path); err != nil {
		return nil, err
	}
	q := RequestFrom(r)
	if q.Repo.Class != repo.Virtual {
		return []*http.Request{r}, nil
	}
	var out []*http.Request
	for _, key := range q.Repo.Repositories {
		member, err := k.o.Store.Repos().Get(r.Context(), key)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			continue
		case err != nil:
			return nil, fmt.Errorf("%s: member %s: %w", k.p.Name, key, err)
		case generic.CheckDownload(member, path, false) != nil:
			continue
		}
		mr, err := k.MemberRequest(r, member)
		if err != nil {
			return nil, err
		}
		out = append(out, mr)
	}
	return out, nil
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
