package docker

import (
	"encoding/json"
	"net/http"
	"strconv"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// filtersHeader names the filters a referrers answer applied (end-12b), so
// a client that asked for one artifact type knows it need not filter again.
const filtersHeader = "OCI-Filters-Applied"

// registerReferrers registers the referrers API (decision 0031) when the
// registry has a blob store, which holds the manifests it lists.
func (g *Registry) registerReferrers() {
	if g.uploads == nil {
		return
	}
	g.handle(KindReferrers, http.MethodGet, g.getReferrers)
}

// referrersIndex is the image index a referrers request is answered with.
//
//nolint:tagliatelle // the OCI image specification's field names
type referrersIndex struct {
	SchemaVersion int                 `json:"schemaVersion"`
	MediaType     string              `json:"mediaType"`
	Manifests     []layout.Descriptor `json:"manifests"`
}

// getReferrers serves GET .../referrers/<digest>[?artifactType=]: an image
// index of the manifests in the image whose subject is the digest, from
// their properties (DV-036). A digest nothing refers to, in an image that
// may not exist, is an empty index: the API answers no 404 (end-12a). A
// reference that is no digest is refused with DIGEST_INVALID.
func (g *Registry) getReferrers(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionRead); err != nil {
		Write(w, err)
		return
	}
	if !layout.ValidDigest(q.Reference) {
		Write(w, Errorf(http.StatusBadRequest, CodeDigestInvalid,
			"invalid digest %q", q.Reference))
		return
	}
	artifactType := r.URL.Query().Get("artifactType")
	found, err := g.uploads.Referrers(r.Context(), owner(r, q), q.Reference, artifactType)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	// Strings, numbers and string maps always marshal.
	body, _ := json.Marshal(referrersIndex{
		SchemaVersion: 2, MediaType: layout.MediaTypeOCIIndex, Manifests: found,
	})
	h := w.Header()
	h.Set("Content-Type", layout.MediaTypeOCIIndex)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if artifactType != "" {
		h.Set(filtersHeader, "artifactType")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
