package npmhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// remoteKeysPath is the upstream's keys document, relative to the remote's
// URL, where npm audit signatures would ask the upstream itself.
const remoteKeysPath = "-/npm/v1/keys"

// remoteKeys answers a remote repository's keys document with its
// upstream's, unchanged, through the shared retrieval contract: the
// remote's credentials, retrieval cache period, offline mode and source
// errors. A remote relays the upstream's dist.signatures (rewriteVersions),
// and npm verifies them only with keys the registry it installed from
// lists: it asks the Sigstore TUF repository for the remote's own host and
// path, which it does not know, then <registry>/-/npm/v1/keys (npm/cli
// verify-signatures.js). 7.x's npm documentation has a remote of a signing
// upstream pass npm audit signatures, "Artifactory provides the signature
// directly from the remote repository", but names no keys answer, so
// relaying the upstream's is inferred [I] (#216).
//
// The document's cache entry is keyed at keysDocumentPath, and it is
// checked there as a local repository's is (DV-042): read permission first,
// then the remote's download policy (DV-057), whose refusal answers its 404
// before the upstream is asked. 7.x's known issue that an include pattern
// must name .npm/keys.json does not say a local repository only.
//
// An upstream with no keys document, or an offline remote with none cached,
// answers the 404 of a repository that lists no keys, which npm reads as a
// registry without keys. A failing upstream answers the source error the
// remote's packuments answer (502, 504, or the upstream's 401, 403, 429 or
// 5xx), so npm fails the audit rather than skip it [I] (#216).
func (h handler) remoteKeys(w http.ResponseWriter, r *http.Request) {
	result, err := h.k.Read(r, source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                keysDocumentPath,
		UpstreamPath:        remoteKeysPath,
		Method:              http.MethodGet,
		Accept:              jsonType,
		ExpectedContentType: jsonType,
		Variant:             "npm-keys-v1",
		Validate:            validateKeysDocument,
	})
	switch {
	case upstreamMissing(err):
		h.k.Error(w, r, notFound(r))
		return
	case err != nil:
		h.k.Error(w, r, err)
		return
	}
	if result.Stream != nil {
		// The bytes are the upstream's, which validateKeysDocument read as
		// JSON, served with their own SHA-1 as ETag as any cached file is
		// (#1484); only the label is the local document's and
		// registry.npmjs.org's, whatever the upstream sent [I] (#216).
		result.Stream.MediaType = jsonType
	}
	h.k.ServeResult(w, r, result)
}

// upstreamMissing reports whether err is the source's 404 for a document
// the upstream does not have. The download policy's refusal is a 404 too,
// with the policy's message, and stays as it is.
func upstreamMissing(err error) bool {
	var answer *apierror.Error
	return errors.As(err, &answer) && answer.Status == http.StatusNotFound &&
		!pkgroute.IsRefusal(err)
}

// validateKeysDocument admits the upstream's keys document to the cache: a
// JSON object whose keys is an array of objects, the shape npm maps over to
// find each signature's key (verify-signatures.js). Anything else answers
// 502 and is not cached, so a later fetch can correct it, as a packument
// that fails validatePackageMetadata does [I] (#216). The entries' fields
// are npm's to read, and are relayed as they came.
func validateKeysDocument(_ context.Context, r io.ReadSeeker, size int64) error {
	if size > remoteMetadataLimit {
		return remoteProtocolError()
	}
	doc, err := decodeRemoteDocument(r)
	if err != nil {
		return err
	}
	var keys []map[string]json.RawMessage
	if json.Unmarshal(doc["keys"], &keys) != nil || keys == nil {
		return remoteProtocolError()
	}
	for _, key := range keys {
		if key == nil {
			return remoteProtocolError()
		}
	}
	return nil
}
