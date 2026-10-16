package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tagHeader names, once per tag, the tags a push by digest stored with
// ?tag= (end-7b); subjectHeader the subject of a manifest pushed with one.
const (
	tagHeader     = "OCI-Tag"
	subjectHeader = "OCI-Subject"
)

// registerManifests registers manifest pushes, pulls and deletes (decision
// 0031) when the registry has a blob store, which holds the manifests'
// bytes.
func (g *Registry) registerManifests() {
	if g.uploads == nil {
		return
	}
	g.handle(KindManifest, http.MethodHead, g.getManifest)
	g.handle(KindManifest, http.MethodGet, g.getManifest)
	g.handle(KindManifest, http.MethodPut, g.putManifest)
	g.handle(KindManifest, http.MethodDelete, g.deleteManifest)
}

// getManifest serves HEAD and GET of a manifest by tag or by digest, with
// the media type it was pushed with. Manifests never redirect.
func (g *Registry) getManifest(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionRead); err != nil {
		Write(w, err)
		return
	}
	it, err := g.findManifest(r, q)
	if errors.Is(err, store.ErrNotFound) {
		Write(w, Errorf(http.StatusNotFound, CodeManifestUnknown,
			"manifest unknown to registry: %s", q.Reference))
		return
	}
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	content, _, err := g.blobs.Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	defer func() { _ = content.Close() }()
	mediaType, err := g.manifestType(r, it, content)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	if !accepts(r.Header.Values("Accept"), mediaType) {
		Write(w, Errorf(http.StatusNotFound, CodeManifestUnknown,
			"the manifest is %s, which the Accept header does not include", mediaType))
		return
	}
	digest := "sha256:" + it.Actual.SHA256
	h := w.Header()
	h.Set("Content-Type", mediaType)
	h.Set(contentDigestHeader, digest)
	h.Set("ETag", `"`+digest+`"`)
	http.ServeContent(w, r, "", it.Modified, content)
}

// findManifest finds the manifest a read names: by tag, the manifest in
// the tag folder; by digest, the manifest with that SHA-256 in any of the
// image's own folders. A reference that is no valid tag or digest, a
// reserved name and a digest under another algorithm are not found.
func (g *Registry) findManifest(r *http.Request, q Request) (types.Item, error) {
	ctx := r.Context()
	if strings.Contains(q.Reference, ":") {
		hex, ok := layout.SHA256Hex(q.Reference)
		if !ok {
			return types.Item{}, store.ErrNotFound
		}
		it, err := layout.FindManifest(ctx, g.store.Items(), q.Repo.Key, q.Name, hex)
		return it, err //nolint:wrapcheck // the layout names itself
	}
	if !layout.ValidTag(q.Reference) || layout.ReservedTag(q.Reference) {
		return types.Item{}, store.ErrNotFound
	}
	for _, file := range []string{layout.ManifestFile, layout.ListManifestFile} {
		it, err := g.store.Items().Get(ctx, q.Repo.Key, false,
			q.Name+"/"+q.Reference+"/"+file)
		if !errors.Is(err, store.ErrNotFound) {
			return it, err //nolint:wrapcheck // store errors name themselves
		}
	}
	return types.Item{}, store.ErrNotFound
}

// manifestType is the media type a manifest is served with: the one it was
// pushed with, kept as a property. A manifest.json deployed through the
// REST API has none, so its own mediaType names it, or failing that the
// file name does.
func (g *Registry) manifestType(
	r *http.Request,
	it types.Item,
	content io.ReadSeeker,
) (string, error) {
	props, err := g.store.Items().Properties(r.Context(), it.ID)
	if err != nil {
		return "", err //nolint:wrapcheck // store errors name themselves
	}
	if v := props[layout.ManifestTypeProperty]; len(v) > 0 {
		return v[0], nil
	}
	var body struct {
		MediaType string `json:"mediaType"` //nolint:tagliatelle // the OCI specification's name
	}
	_ = json.NewDecoder(io.LimitReader(content, layout.MaxManifestSize)).Decode(&body)
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return "", err //nolint:wrapcheck // the blob store's own error
	}
	switch {
	case body.MediaType != "":
		return body.MediaType, nil
	case it.Name == layout.ListManifestFile:
		return layout.MediaTypeOCIIndex, nil
	default:
		return layout.MediaTypeOCIManifest, nil
	}
}

// accepts reports whether a client sending accept may be answered with a
// manifest of mediaType. Clients list every manifest type they read, so
// one that lists manifest types but not this one cannot read it, and is
// told the manifest is unknown, as the reference registry does. A client
// that lists none, or a wildcard, takes what is stored.
func accepts(accept []string, mediaType string) bool {
	listed := false
	for _, header := range accept {
		for v := range strings.SplitSeq(header, ",") {
			t, _, err := mime.ParseMediaType(strings.TrimSpace(v))
			if err != nil {
				continue
			}
			switch {
			case t == mediaType || t == "*/*" || t == "application/*":
				return true
			case slices.Contains(manifestTypes, t):
				listed = true
			}
		}
	}
	return !listed
}

// manifestTypes are the media types the registry stores manifests as.
var manifestTypes = []string{
	layout.MediaTypeOCIManifest, layout.MediaTypeOCIIndex,
	layout.MediaTypeDockerManifest, layout.MediaTypeDockerList,
}

// putManifest serves PUT of an image manifest, image index or manifest
// list by tag or by digest, with ?tag= for more tags on a push by digest
// (end-7b). It answers 201 with the manifest's digest and location, and
// its subject when it has one.
func (g *Registry) putManifest(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionWrite); err != nil {
		Write(w, err)
		return
	}
	byDigest, err := pushReference(q.Reference)
	if err != nil {
		Write(w, err)
		return
	}
	body, err := readManifest(r)
	if err != nil {
		Write(w, err)
		return
	}
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])
	if byDigest && q.Reference != "sha256:"+hexSum {
		Write(w, Errorf(http.StatusBadRequest, CodeDigestInvalid,
			"the manifest's digest is sha256:%s, not %s", hexSum, q.Reference))
		return
	}
	m, err := layout.ParseManifest(r.Header.Get("Content-Type"), body, hexSum)
	if err != nil {
		Write(w, Errorf(http.StatusBadRequest, CodeManifestInvalid, "%s", manifestMessage(err)))
		return
	}
	folders := []string{q.Reference}
	var tags []string
	if byDigest {
		if tags, err = extraTags(r); err != nil {
			Write(w, err)
			return
		}
		folders = append([]string{layout.DigestFolder(hexSum)}, tags...)
	}
	o := owner(r, q)
	for _, f := range folders {
		replaces, err := g.uploads.Replaces(r.Context(), o, f, hexSum)
		if err == nil && replaces {
			// Strict Tag Overwrite (DV-035).
			err = q.Require(r, q.Name+"/"+f+"/", types.ActionDelete)
		}
		if err != nil {
			g.writeErr(w, r, err)
			return
		}
	}
	err = g.uploads.PutManifest(r.Context(), registry.Push{
		Owner: o, Manifest: m, Body: body, Folders: folders,
	})
	if err != nil {
		g.manifestErr(w, r, q, err)
		return
	}
	h := w.Header()
	h.Set("Location", location(r, q, "manifests", "sha256:"+hexSum))
	h.Set(contentDigestHeader, "sha256:"+hexSum)
	if m.Subject != nil {
		// Tells the client the registry tracks referrers itself, so it
		// keeps no referrers tag of its own (end-7, Artifactory 7.90.1).
		h.Set(subjectHeader, m.Subject.Digest)
	}
	for _, t := range tags {
		h.Add(tagHeader, t)
	}
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// pushReference checks the reference a push names and reports whether it
// is a digest. A reference that is neither a valid tag nor a valid digest
// answers MANIFEST_INVALID; a tag that would collide with the layout's own
// folders TAG_INVALID; a digest under another algorithm than sha256, which
// the registry cannot verify, DIGEST_INVALID.
func pushReference(ref string) (bool, error) {
	if strings.Contains(ref, ":") {
		if !layout.ValidDigest(ref) {
			return false, Errorf(http.StatusBadRequest, CodeManifestInvalid,
				"invalid manifest reference %q", ref)
		}
		if _, ok := layout.SHA256Hex(ref); !ok {
			return false, Errorf(http.StatusBadRequest, CodeDigestInvalid,
				"unsupported digest %q: only sha256 is supported", ref)
		}
		return true, nil
	}
	return false, checkTag(ref)
}

// checkTag refuses a tag outside the grammar with MANIFEST_INVALID and a
// reserved one with TAG_INVALID.
func checkTag(tag string) error {
	switch {
	case !layout.ValidTag(tag):
		return Errorf(http.StatusBadRequest, CodeManifestInvalid, "invalid tag %q", tag)
	case layout.ReservedTag(tag):
		return Errorf(http.StatusBadRequest, CodeTagInvalid,
			"%q names a folder the registry keeps for itself and cannot be a tag", tag)
	default:
		return nil
	}
}

// extraTags are the distinct tags ?tag= names on a push by digest.
func extraTags(r *http.Request) ([]string, error) {
	var tags []string
	for _, t := range r.URL.Query()["tag"] {
		if err := checkTag(t); err != nil {
			return nil, err
		}
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// readManifest reads a pushed manifest, refusing one over the size limit
// with 413 SIZE_INVALID before or while reading it.
func readManifest(r *http.Request) ([]byte, error) {
	tooLarge := Errorf(http.StatusRequestEntityTooLarge, CodeSizeInvalid,
		"a manifest may hold at most %d bytes", layout.MaxManifestSize)
	if r.ContentLength > layout.MaxManifestSize {
		return nil, tooLarge
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, layout.MaxManifestSize+1))
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeManifestInvalid,
			"the manifest could not be read")
	}
	if len(body) > layout.MaxManifestSize {
		return nil, tooLarge
	}
	return body, nil
}

// manifestMessage is the part of a refusal from the layout a client reads.
func manifestMessage(err error) string {
	msg := err.Error()
	if _, after, ok := strings.Cut(msg, layout.ErrManifestInvalid.Error()+": "); ok {
		return after
	}
	return msg
}

// manifestErr answers what storing a manifest refused: one
// MANIFEST_BLOB_UNKNOWN for each blob, or each manifest an index lists, that
// the image does not hold.
func (g *Registry) manifestErr(w http.ResponseWriter, r *http.Request, q Request, err error) {
	var unknown *registry.BlobsUnknownError
	switch {
	case errors.As(err, &unknown):
		errs := make([]*apierror.Error, 0, len(unknown.Digests))
		for _, d := range unknown.Digests {
			errs = append(errs, &apierror.Error{
				Code: CodeManifestBlobUnknown, Message: "blob unknown to registry", Detail: d,
			})
		}
		WriteAll(w, http.StatusBadRequest, errs...)
	case errors.Is(err, layout.ErrManifestInvalid):
		Write(w, Errorf(http.StatusBadRequest, CodeManifestInvalid, "%s", manifestMessage(err)))
	case errors.Is(err, item.ErrConflict):
		Write(w, Errorf(http.StatusBadRequest, CodeNameInvalid,
			"%s cannot hold an image: a file is in the way", q.Name))
	default:
		g.writeErr(w, r, err)
	}
}
