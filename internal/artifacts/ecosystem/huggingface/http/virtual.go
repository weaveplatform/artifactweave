package huggingfacehttp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	formatapi "github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	format "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/huggingface"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// selectMember chooses the one member of the virtual repository of r that
// serves revision rev of the model or dataset id, for the whole revision,
// so a client never combines files of two members.
//
// The virtual repository's own include and exclude patterns come first, an
// administrator's too: a model folder they reject answers 404 "No
// repository found to serve the request for <key>:<folder>" before any
// member is asked (decision 0008, R5). The members are the package
// handlers' walk (pkgroute.Kit.MergeMembers), with nested virtual
// repositories flattened under one visited set, so a cycle, a self-member
// or a duplicate is skipped without an error (R2), and a member of another
// type passed over (R11). A path is not known before a member is chosen,
// so the walk checks blackout alone, as a document of no documented 7.x
// storage path does, and the model's folder then drops a nested virtual
// repository whose patterns reject it and passes over a member whose own
// policy does (R2, R5, R10).
//
// A member in which the caller may read nothing of the model, one without
// the revision and one whose upstream fails pass over, the last logged
// (R3) [I]. A member still on the legacy layout holds no snapshot path, so
// it misses and passes over: JFrog's Hugging Face page says virtual
// repositories support only members on the new layout [I]. Any other
// error ends the walk.
func (h handler) selectMember(r *http.Request, id format.Identity, rev string) (selected, error) {
	v := pkgroute.RequestFrom(r).Repo
	folder := id.Prefix()
	// A virtual repository's only refusal is its patterns'. The kit answers
	// only an *apierror.Error; the bare refusal would be its 500.
	var refused *formatapi.RejectionError
	if errors.As(generic.CheckDownload(v, folder, true), &refused) {
		return selected{}, apierror.New(refused.Status, refused.Message)
	}
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		return selected{}, fmt.Errorf("huggingface: virtual members: %w", err)
	}
	steps, err := virtual.Members(r.Context(), h.k.DB().Repos(), pkgroute.RequestFrom(r).Repo,
		virtual.Options{Order: virtual.Package, Path: folder, Folder: true})
	if err != nil {
		return selected{}, fmt.Errorf("huggingface: virtual members: %w", err)
	}
	// MergeMembers' walk without a path keeps a nested virtual repository
	// whose own patterns reject folder; this walk with it drops its members
	// (decision 0008, R2).
	admitted := make(map[string]bool, len(steps))
	for _, m := range steps {
		admitted[m.Repo.Key] = true
	}
	readable := false
	for _, mr := range members {
		m := pkgroute.RequestFrom(mr).Repo
		if !admitted[m.Key] || generic.CheckDownload(m, folder, true) != nil ||
			!h.k.CanFolder(mr, folder, api.ActionRead) {
			continue
		}
		readable = true
		chosen, err := h.selectRepo(mr, id, rev)
		switch {
		case err == nil:
			return chosen, nil
		case errors.Is(err, kernel.ErrNotFound) || isNotFound(err):
		case pkgroute.PassedOver(err):
			h.logFault(r, mr, folder, err)
		default:
			return selected{}, err
		}
	}
	return selected{}, h.unserved(r, folder, readable, steps)
}

// unserved is the answer to a read of folder through the virtual repository
// of r that no member served: 404 "Hugging Face revision or file not
// found", for a caller who may read the model in a member and for one who
// may read it in none, since a package handler counts an unreadable member
// as not found (decision 0008, R3), where the second got 403 at the first
// such member. An anonymous caller who may read it in no member gets the
// 401 challenge, as 7.x answers an anonymous caller its walk refused (R4)
// [I], unless the virtual repository or the security configuration hides
// unauthorized resources (#1903) or no member of steps refused anyone: none
// of the type whose own policy admits folder.
func (h handler) unserved(r *http.Request, folder string, readable bool, steps []virtual.Member) error {
	v := pkgroute.RequestFrom(r).Repo
	p, _ := kernel.FromContext(r.Context())
	if readable || !p.Anonymous || v.HideUnauthorizedResources != nil && *v.HideUnauthorizedResources {
		return missing()
	}
	switch hide, err := httpkit.Hides(r.Context()); {
	case err != nil:
		return err //nolint:wrapcheck // names the setting
	case hide:
		return missing()
	}
	for _, m := range steps {
		// MemberRequest refuses only a member of another type or of a class
		// with no Source, which the walk passes over.
		if _, err := h.k.MemberRequest(r, m.Repo); err == nil &&
			generic.CheckDownload(m.Repo, folder, true) == nil {
			return apierror.New(http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
		}
	}
	return missing()
}

// deployTarget returns the member request a write of id through the
// virtual repository of r lands in: its defaultDeploymentRepo, a local or
// federated repository (virtual.DeployTarget), with Write asked on the
// model's folder there alone, since no permission target names a virtual
// repository; 7.x checks no permission on the virtual repository's key.
// Without one the answer is 7.x's 405 with Allow: GET and "No local
// repository was configured as local deployment repository for the (<key>)
// virtual repository." before any permission is asked (decision 0008, R6).
func (h handler) deployTarget(r *http.Request, id format.Identity) (*http.Request, error) {
	v := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	switch {
	case errors.Is(err, virtual.ErrNoDeployTarget):
		return nil, fmt.Errorf("%w: %w", err, itemkit.NoDeployTarget(v.Key))
	case err != nil:
		return nil, fmt.Errorf("huggingface: deployment target: %w", err)
	}
	// DeployTarget returns only a local or federated repository, so
	// MemberRequest refuses only one of another type, which only a
	// configuration saved before the save checks names (repos.CheckMembers).
	mr, err := h.k.MemberRequest(r, target)
	if err != nil {
		return nil, apierror.New(http.StatusBadRequest, "invalid virtual model deployment target")
	}
	if !h.k.CanFolder(mr, id.Prefix(), api.ActionWrite) {
		return nil, apierror.New(http.StatusForbidden, "model deployment target is not authorized")
	}
	return mr, nil
}

// logFault logs a member the walk passed over for a fault of its upstream,
// an answer of 500 or more or one that never came, which 7.x's walk passes
// over as it passes over a miss (decision 0008, R3) [I]: only the log tells
// an operator why the revision came from a later member or from none.
func (h handler) logFault(r, member *http.Request, folder string, err error) {
	// Every remote read passes through pkgroute.SourceError, which answers
	// an upstream's 5xx, an unreachable upstream and a timeout with 500 or
	// more; anything else the walk passed over is a miss or a refusal.
	var answer *apierror.Error
	if !errors.As(err, &answer) || answer.Status < http.StatusInternalServerError {
		return
	}
	h.o.Logger.WarnContext(r.Context(), "huggingface: virtual repository member passed over",
		"virtual", pkgroute.RequestFrom(r).Repo.Key,
		"member", pkgroute.RequestFrom(member).Repo.Key,
		"path", folder, "error", err)
}
