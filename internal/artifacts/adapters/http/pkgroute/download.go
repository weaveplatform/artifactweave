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

// CheckDownload applies to path the download policy that Item applies, for
// a protocol that reads a stored file's blob itself, as Bower's archive of
// a version's files does: nil, or the refusal Item would return. The caller
// checks read permission first.
func (k *Kit) CheckDownload(r *http.Request, path string) error {
	if err := RequestFrom(r).download(path); err != nil {
		return fmt.Errorf("%s: %w", k.p.Name, err)
	}
	return nil
}

// download refuses what Artifactory refuses before it looks for the file a
// download names, whether or not it exists and for an administrator too
// (generic.CheckDownload, decision 0032): any path of a blacked-out
// repository, and a path the repository's include and exclude patterns
// reject. A member request checks the virtual repository it came through
// first, whose patterns rejecting the path leave it no member to search.
// A member that refuses is then passed over as one without the file is, so
// the answer is the virtual repository's "not found", which says nothing of
// how the member is set. A remote repository's own policy is not checked
// here: the source boundary checks its reads, and Remote has no Item.
func (q Request) download(path string) error {
	if q.virtual != nil {
		if err := refused(generic.CheckDownload(*q.virtual, path, false)); err != nil {
			return err
		}
	}
	if q.Repo.Class == repo.Remote {
		return nil
	}
	err := generic.CheckDownload(q.Repo, path, false)
	if err != nil && q.virtual != nil {
		// Both, so a search passes over the member and a handler that hands
		// the error to Error answers the 404 rather than a fault.
		return fmt.Errorf(
			"download %s:%s: %w: %w",
			q.Repo.Key,
			path,
			kernel.ErrNotFound,
			notFound(),
		)
	}
	return refused(err)
}

// refusedError is a download the policy refuses. Protocols treat it as
// kernel.ErrNotFound, so a virtual repository's search passes over the
// refusing member and a deploy's check for an existing file goes on as
// before, where Admit refuses the deploy itself. Error finds its 404 as an
// *apierror.Error, and SourceError as the *format.RejectionError, so both
// answer with CheckDownload's message.
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
