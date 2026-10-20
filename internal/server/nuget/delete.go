package nuget

import (
	"context"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// target is the version a delete or a relist names, /{id}/{version} under
// the v2 root, which the caller may read; it answers 404 otherwise, so a
// version the caller may not read is not revealed. Any other number of
// segments answers 404, as 7.x answers a delete through a folder
// (RTFACT-23310).
func (h handler) target(w http.ResponseWriter, r *http.Request) (version, bool) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return version{}, false
	}
	if len(p.Segments) != 2 {
		h.k.Error(w, r, notFound(r))
		return version{}, false
	}
	id, ver := p.Segments[0], p.Segments[1]
	key, ok := versionKey(ver)
	if !ok {
		h.k.Error(w, r, notFound(r))
		return version{}, false
	}
	v, ok, err := h.indexed(r, id, key)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return version{}, false
	case !ok || !h.k.Can(r, v.meta.Path, types.ActionRead):
		h.k.Error(w, r, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Could not find %s %s", id, ver)))
		return version{}, false
	}
	return v, true
}

// remove answers DELETE /api/nuget/{repoKey}/{id}/{version} (decision
// 0038, "Delete and relist"): a hard delete, as on 7.x (RTFACT-16148), of
// every file of the version, its .nupkg and its .snupkg, through the trash
// can. The caller needs Delete on each, checked for all before any goes,
// and the files go in one transaction. It answers 204; [I] the status is
// inferred.
func (h handler) remove(w http.ResponseWriter, r *http.Request) {
	v, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	st := h.k.DB()
	ids, err := st.Packages().Files(ctx, v.pv.ID)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("nuget: files of %s: %w", v.meta.ID, err))
		return
	}
	paths := make([]string, 0, len(ids))
	for _, id := range ids {
		it, err := st.Items().GetByID(ctx, id)
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("nuget: file %d of %s: %w", id, v.meta.ID, err))
			return
		}
		if !h.k.Require(w, r, it.FullPath(), types.ActionDelete) {
			return
		}
		paths = append(paths, it.FullPath())
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		for _, p := range paths {
			if err := h.k.Delete(r.WithContext(ctx), p); err != nil {
				return err //nolint:wrapcheck // the kit's answer, given as it is
			}
		}
		return nil
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// relist answers POST /api/nuget/{repoKey}/{id}/{version}. There is no
// unlisted state, as on 7.x, so the version is listed already and nothing
// changes: it answers 200 with no body to a caller with Annotate on the
// version's .nupkg [I], the right a listing change would need.
func (h handler) relist(w http.ResponseWriter, r *http.Request) {
	v, ok := h.target(w, r)
	if !ok || !h.k.Require(w, r, v.meta.Path, types.ActionAnnotate) {
		return
	}
	w.WriteHeader(http.StatusOK)
}
