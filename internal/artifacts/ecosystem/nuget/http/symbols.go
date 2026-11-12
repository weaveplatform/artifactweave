package nugethttp

import (
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (h handler) symbolFile(w http.ResponseWriter, r *http.Request) {
	segs, err := segments(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(segs) == 3 {
		if p, ok := nugetfmt.SymbolPath(segs[0], segs[1], segs[2]); ok {
			if !h.k.Require(w, r, p, api.ActionRead) {
				return
			}
			// Freshness precedes missing-item lookup: a newly pushed PDB may
			// not have been extracted by a background worker yet. Only a
			// missing PDB answers the protocol's own 404: the download
			// policy's refusal is kernel.ErrNotFound too, and passes on with
			// its own message, as on every download (decision 0032).
			if _, err := h.k.ResolveDownloadItem(r, p); err != nil {
				if errors.Is(err, kernel.ErrNotFound) && !pkgroute.IsRefusal(err) {
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
