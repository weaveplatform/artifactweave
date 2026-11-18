package dockerhttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
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
// reference that is no digest is refused with DIGEST_INVALID. A blacked-out
// repository refuses the request first, as a blob pull checks blackout
// before its digest (imageBlackedOut).
func (g *Registry) getReferrers(w http.ResponseWriter, r *http.Request, q Request) {
	var err error
	if q.Repo.Class == repo.Virtual {
		err = g.requireVirtual(r, q)
	} else {
		err = q.Require(r, q.Name+"/", api.ActionRead)
	}
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	if imageBlackedOut(w, q) {
		return
	}
	if !layout.ValidDigest(q.Reference) {
		Write(w, Errorf(http.StatusBadRequest, CodeDigestInvalid,
			"invalid digest %q", q.Reference))
		return
	}
	artifactType := r.URL.Query().Get("artifactType")
	var found []layout.Descriptor
	if q.Repo.Class == repo.Virtual {
		found, err = g.virtualReferrers(r, q, artifactType)
	} else {
		found, err = g.uploads.Referrers(r.Context(), owner(r, q), q.Reference, artifactType)
	}
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

func (g *Registry) virtualReferrers(r *http.Request, q Request, artifactType string) ([]layout.Descriptor, error) {
	members, _, err := layout.VirtualMembers(r.Context(), g.store.Repos(), q.Repo, q.Name)
	if err != nil {
		return nil, fmt.Errorf("virtual referrer members: %w", err)
	}
	d, _ := api.DeciderFrom(r.Context())
	parts := []layout.VirtualReferrersPart{}
	for _, member := range members {
		if (member.Repo.Class != repo.Local && member.Repo.Class != repo.Federated) || !memberReadable(d, member.Repo, q.Name) {
			continue
		}
		found, err := g.uploads.Referrers(r.Context(), registry.Owner{Repo: member.Repo, Name: q.Name}, q.Reference, artifactType)
		if err != nil {
			return nil, fmt.Errorf("virtual referrers in %s: %w", member.Repo.Key, err)
		}
		parts = append(parts, layout.VirtualReferrersPart{Member: member, Manifests: found})
	}
	return layout.MergeVirtualReferrers(parts), nil
}
