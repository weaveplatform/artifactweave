package gitlfshttp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	format "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gitlfs"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// errChallenge is the answer to an anonymous caller who may read an object
// in no member of a virtual repository: the 401 challenge, so git-lfs asks
// its credential helper and retries, where an authenticated caller gets
// the object's 404 (unserved). A download batch answers it for the whole
// batch, since git-lfs fills credentials only for a 401 of the request,
// never for an object's error code.
var errChallenge = apierror.New(http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))

// virtualMember returns the member request that serves obj through the
// virtual repository of r: the first member of the package handlers' walk
// (pkgroute.Kit.MergeMembers) in which the caller may read obj's path and
// which holds obj.
//
// MergeMembers refuses a path the virtual repository's own include and
// exclude patterns reject before any member is asked, an administrator's
// too: 404 "No repository found to serve the request for <key>:<path>"
// (decision 0008, R5). It flattens nested virtual repositories with one
// visited set, so a cycle, a self-member or a duplicate is skipped without
// an error (R2), and it passes over a member of another type (R11), one
// whose own policy refuses the path (R5, R10) and one the caller may read
// nothing in. A member that misses obj, one in which the caller may not
// read obj's path, and one whose upstream fails pass over too (R3): 7.x
// serves an object that only a later member holds, a remote member's
// included (RTFACT-12951, RTFACT-14494), and a package handler counts an
// unreadable member as one without the object (RTDEV-88743). Any other
// error, such as a member's object of another size, ends the walk.
func (h handler) virtualMember(r *http.Request, obj batchObject) (*http.Request, error) {
	path, _ := format.ObjectPath(obj.OID)
	members, err := h.k.MergeMembers(r, path)
	if err != nil {
		return nil, fmt.Errorf("gitlfs: virtual members: %w", err)
	}
	readable := false
	for _, member := range members {
		if !h.k.Can(member, path, api.ActionRead) {
			continue
		}
		readable = true
		err = h.available(member, obj)
		switch {
		case err == nil:
			return member, nil
		case missing(err):
		case pkgroute.PassedOver(err):
			h.logFault(r, member, path, err)
		default:
			return nil, err
		}
	}
	return nil, h.unserved(r, path, readable)
}

// unserved is the answer to a read of path through the virtual repository
// of r that no member served: kernel.ErrNotFound, the object's 404, for a
// caller who may read path in a member and for one who may read it in
// none, since a package handler counts an unreadable member as not found
// (decision 0008, R3). An anonymous caller who may read path in no member
// gets errChallenge instead, as 7.x answers an anonymous caller its walk
// refused (R4) [I]; a virtual repository that hides unauthorized resources
// answers the 404 to it too. A virtual repository with no member to read
// refused nobody, so it answers the 404.
func (h handler) unserved(r *http.Request, path string, readable bool) error {
	v := pkgroute.RequestFrom(r).Repo
	p, _ := kernel.FromContext(r.Context())
	if readable || !p.Anonymous || v.HideUnauthorizedResources != nil && *v.HideUnauthorizedResources {
		return kernel.ErrNotFound
	}
	members, err := h.walk(r, path)
	if err != nil {
		return err
	}
	for _, m := range members {
		// A member whose own policy refuses path passes over before Read is
		// asked of it, so it refused the caller nothing (R10).
		if generic.CheckDownload(pkgroute.RequestFrom(m).Repo, path, false) == nil {
			return errChallenge
		}
	}
	return kernel.ErrNotFound
}

// walk returns the real Git LFS members of the virtual repository of r,
// each as its member request, in 7.x's effective order (virtual.Members,
// virtual.Package): nested virtual repositories flattened with one visited
// set, so a cycle, a self-member or a duplicate is skipped without an error
// (decision 0008, R1 and R2), and a member of another type, or of a class
// the protocol has no Source for, passed over (R11). path, when set, drops
// a nested virtual repository whose own patterns reject it.
//
// The lock routes walk it rather than MergeMembers' list: a lock is no
// download, so neither the download policy nor the caller's Read leaves a
// member out of it, and verification fails closed on a member the caller
// may not read rather than skipping it.
func (h handler) walk(r *http.Request, path string) ([]*http.Request, error) {
	v := pkgroute.RequestFrom(r).Repo
	steps, err := virtual.Members(r.Context(), h.k.DB().Repos(), v,
		virtual.Options{Order: virtual.Package, Path: path})
	if err != nil {
		return nil, fmt.Errorf("gitlfs: virtual members: %w", err)
	}
	out := make([]*http.Request, 0, len(steps))
	for _, m := range steps {
		// MemberRequest refuses only a member of another type or of a class
		// with no Source, which the walk passes over.
		if mr, err := h.k.MemberRequest(r, m.Repo); err == nil {
			out = append(out, mr)
		}
	}
	return out, nil
}

// deployTarget returns the member request a write through the virtual
// repository of r lands in: its defaultDeploymentRepo, a local or federated
// repository (virtual.DeployTarget), as 7.x deploys through a virtual
// repository to its default deployment repository (RTFACT-12951). Without
// one the answer is 7.x's 405 with Allow: GET and "No local repository was
// configured as local deployment repository for the (<key>) virtual
// repository." before any permission is asked (decision 0008, R6), which
// wraps virtual.ErrNoDeployTarget. The caller then asks Write on the
// target's path alone: no permission target names a virtual repository.
func (h handler) deployTarget(r *http.Request) (*http.Request, error) {
	v := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	switch {
	case errors.Is(err, virtual.ErrNoDeployTarget):
		return nil, fmt.Errorf("%w: %w", err, itemkit.NoDeployTarget(v.Key))
	case err != nil:
		return nil, fmt.Errorf("gitlfs: deployment target: %w", err)
	}
	// DeployTarget returns only a local or federated repository, so
	// MemberRequest refuses only one of another type, which only a
	// configuration saved before the save checks names (repos.CheckMembers).
	mr, err := h.k.MemberRequest(r, target)
	if err != nil {
		return nil, apierror.New(http.StatusBadRequest, "invalid Git LFS deployment target")
	}
	return mr, nil
}

// logFault logs a member the walk passed over for a fault of its upstream,
// an answer of 500 or more or one that never came, which 7.x's walk passes
// over as it passes over a miss (decision 0008, R3) [I]: only the log tells
// an operator why the object came from a later member or from none. A miss
// or a refusal is the walk's ordinary answer and is not logged.
func (h handler) logFault(r, member *http.Request, path string, err error) {
	// Every remote read passes through pkgroute.SourceError, which answers
	// an upstream's 5xx, an unreachable upstream and a timeout with 500 or
	// more; anything else the walk passed over is a miss or a refusal.
	var answer *apierror.Error
	if !errors.As(err, &answer) || answer.Status < http.StatusInternalServerError {
		return
	}
	h.o.Logger.WarnContext(r.Context(), "gitlfs: virtual repository member passed over",
		"virtual", pkgroute.RequestFrom(r).Repo.Key,
		"member", pkgroute.RequestFrom(member).Repo.Key,
		"path", path, "error", err)
}
