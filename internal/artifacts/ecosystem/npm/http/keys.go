package npmhttp

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
)

// keysPath is the path, under the registry, of the keys document npm audit
// signatures reads when the Sigstore TUF repository does not know the
// registry: npm/cli's verify-signatures.js fetches "/-/npm/v1/keys" relative
// to the registry each package came from, a scope's own included.
var keysPath = []string{"-", "npm", "v1", "keys"}

// keysDocumentPath is where 7.x keeps a local repository's keys document:
// its npm documentation has an include pattern name ".npm/keys.json" for
// npm audit signatures to work. Nothing is stored there (DV-042), so read
// permission and the download policy are checked at the path, as for a
// packument (R1).
const keysDocumentPath = ".npm/keys.json"

// keysDocument is the keys endpoint's body, the shape of registry.npmjs.org's.
type keysDocument struct {
	Keys []npmfmt.Key `json:"keys"`
}

// keys answers the keys endpoint of a repository that signs, and reports
// whether it answered. A repository that names no ECDSA key pair lists no
// key, and the path then answers as it did before signing existed: 404,
// which npm reads as a registry with no keys. [I] What 7.x answers there
// is unknown (#216).
//
// The caller needs read permission at keysDocumentPath, and the download
// policy is checked there after it (DV-057): a blacked-out repository, or
// an include or exclude pattern on the path, refuses the document with the
// policy's 404.
func (h handler) keys(w http.ResponseWriter, r *http.Request) bool {
	listed, err := npmfmt.Keys(r.Context(), h.keyPairs, pkgroute.RequestFrom(r).Repo)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return true
	case len(listed) == 0:
		return false
	}
	if !h.k.Require(w, r, keysDocumentPath, api.ActionRead) {
		return true
	}
	if err := h.k.CheckDocument(r, keysDocumentPath); err != nil {
		h.k.Error(w, r, err)
		return true
	}
	writeJSON(w, http.StatusOK, jsonType, keysDocument{Keys: listed})
	return true
}
