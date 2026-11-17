package itemkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// errNoPackageStore is a kit built without the router kit's store
// (Deps.PackageOptions), which no virtual repository's walk can read
// members in.
var errNoPackageStore = errors.New("itemkit: no store for the package handlers' walk")

// Packages returns the router kit of p for an overlay that merges a
// virtual repository's index from the package handlers' walk
// (pkgroute.ItemKit), over PackageOptions, logging to the kit's logger
// where they name none. Without keep the kit has no merged metadata
// cache, so its Merged builds on every request: a format whose virtual
// repositories carry no virtualRetrievalCachePeriodSecs, such as Opkg,
// recomputes its index as 7.x does (decision 0008, R8).
func (s *Kit) Packages(p pkgroute.Protocol, keep bool) (*pkgroute.Kit, error) {
	o := s.PackageOptions
	if o.Store == nil {
		return nil, errNoPackageStore
	}
	if o.Logger == nil {
		o.Logger = s.Log()
	}
	if !keep {
		o.Cache = nil
	}
	return pkgroute.ItemKit(o, p), nil
}

// VirtualReads reports whether the caller may read path through the
// virtual repository c, the gate a format overlay asks before it builds c's
// index or serves a package from a member: Read on path in c itself, which
// only an "ANY" target grants, or in a real member a download of path
// walks to (virtual.Members in package order, nested virtual repositories
// flattened). No permission target can name a virtual repository (decision
// 0006), and 7.x decides a read through one by its members' permissions
// (decision 0008, R3), so a caller who may read only the members passes;
// what each member lists is still filtered by the caller's Read in it.
func (a Access) VirtualReads(
	ctx context.Context,
	rs virtual.Repos,
	c repo.Config,
	path string,
) (bool, error) {
	if a.Folder(c, path, accessapi.ActionRead) && a.File(c, path, accessapi.ActionRead) {
		return true, nil
	}
	members, err := virtual.Members(ctx, rs, c, virtual.Options{Order: virtual.Package, Path: path})
	if err != nil {
		return false, fmt.Errorf("members of %s: %w", c.Key, err)
	}
	for _, m := range members {
		if a.Folder(m.Repo, path, accessapi.ActionRead) &&
			a.File(m.Repo, path, accessapi.ActionRead) {
			return true, nil
		}
	}
	return false, nil
}

// VirtualGate asks the virtual repository c's read gate for path
// (Access.VirtualReads) and answers a caller it refuses (VirtualRefused),
// or a member lookup that fails, under op. It reports whether the overlay
// may go on.
func (s *Kit) VirtualGate(
	w http.ResponseWriter, r *http.Request, st Store, c repo.Config, path string,
	p kernel.Principal, acc Access, op string,
) bool {
	ok, err := acc.VirtualReads(r.Context(), st.Repos(), c, path)
	switch {
	case err != nil:
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), op, err))
	case !ok:
		httpkit.WriteErr(w, r, VirtualRefused(c, path, p))
	}
	return err == nil && ok
}

// VirtualRefused is the answer to a caller VirtualReads refuses path
// through the virtual repository c: the 401 or 403 the overlays have
// always given, or, while c's hideUnauthorizedResources is on, 7.x's 404
// "Resource not found" that hides the refusal (decision 0008, R4;
// DownloadServiceImpl.shouldHideUnauthorisedResponse 1499-1518, 7.161.26).
func VirtualRefused(c repo.Config, path string, caller kernel.Principal) error {
	if c.HideUnauthorizedResources != nil && *c.HideUnauthorizedResources {
		return virtual.Answer(c, path, virtual.Resolution{Forbidden: &virtual.Denial{}}, //nolint:wrapcheck // the answer itself
			caller.Anonymous, false)
	}
	return httpkit.Denied(caller)
}
