package nuget

import (
	"errors"
	"net/http"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) symbolFile(w http.ResponseWriter, r *http.Request) {
	segs, err := segments(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(segs) == 3 {
		if p, ok := nugetfmt.SymbolPath(segs[0], segs[1], segs[2]); ok {
			if !h.k.Require(w, r, p, types.ActionRead) {
				return
			}
			// Freshness precedes missing-item lookup: a newly pushed PDB may
			// not have been extracted by a background worker yet.
			if _, err := h.k.ResolveItem(r, p); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					err = notFound(r)
				}
				h.k.Error(w, r, err)
				return
			}
			h.k.ServeItem(w, r, p)
			return
		}
	}
	h.k.Error(w, r, notFound(r))
}
