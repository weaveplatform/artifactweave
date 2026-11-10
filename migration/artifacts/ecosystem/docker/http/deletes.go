package dockerhttp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	layout "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// deleteManifest serves DELETE .../manifests/<tag or digest> (end-9): by
// tag, the tag folder goes; by digest, every folder holding the manifest
// does, its tags with it. It answers 202 with no body. A reference that
// is neither a valid tag nor a valid digest, a reserved name and a digest
// under another algorithm name no manifest the registry stores, and answer
// 404 MANIFEST_UNKNOWN as an absent one does (decision 0031).
func (g *Registry) deleteManifest(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", api.ActionDelete); err != nil {
		Write(w, err)
		return
	}
	err := kernel.ErrNotFound
	o := owner(r, q)
	switch {
	case strings.Contains(q.Reference, ":"):
		if hex, ok := layout.SHA256Hex(q.Reference); ok {
			err = g.uploads.DeleteManifest(r.Context(), o, hex)
		}
	case layout.ValidTag(q.Reference) && !layout.ReservedTag(q.Reference):
		err = g.uploads.DeleteTag(r.Context(), o, q.Reference)
	}
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		Write(w, manifestUnknown(q))
	case err != nil:
		g.writeErr(w, r, err)
	default:
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusAccepted)
	}
}
