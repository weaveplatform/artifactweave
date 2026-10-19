package docker

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Headers of the blob operations (OCI Distribution Specification v1.1).
const (
	contentDigestHeader = "Docker-Content-Digest"
	uploadUUIDHeader    = "Docker-Upload-UUID"
	chunkMinHeader      = "OCI-Chunk-Min-Length"
)

// registerBlobs registers blob pulls and uploads (decision 0031) when the
// registry has a blob store.
func (g *Registry) registerBlobs() {
	if g.uploads == nil {
		return
	}
	g.handle(KindBlob, http.MethodHead, g.getBlob)
	g.handle(KindBlob, http.MethodGet, g.getBlob)
	g.handle(KindBlob, http.MethodDelete, deleteBlob)
	g.handle(KindUploads, http.MethodPost, g.startUpload)
	g.handle(KindUpload, http.MethodGet, g.uploadStatus)
	g.handle(KindUpload, http.MethodPatch, g.appendUpload)
	g.handle(KindUpload, http.MethodPut, g.finishUpload)
	g.handle(KindUpload, http.MethodDelete, g.cancelUpload)
}

// getBlob serves HEAD and GET of a blob in the image. HEAD answers from the
// item; GET streams it, or redirects to a signed URL when the repository's
// download redirect says so (decision 0029).
func (g *Registry) getBlob(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionRead); err != nil {
		Write(w, err)
		return
	}
	if g.blackedOut(w, r, q) {
		return
	}
	hex, err := blobDigest(q.Reference)
	if err != nil {
		Write(w, err)
		return
	}
	it, err := layout.FindBlob(r.Context(), g.store.Items(), q.Repo.Key, q.Name, hex)
	if errors.Is(err, store.ErrNotFound) {
		Write(w, blobUnknown(q.Reference))
		return
	}
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	if excluded(w, q, it, CodeBlobUnknown) {
		return
	}
	h := w.Header()
	h.Set(contentDigestHeader, "sha256:"+hex)
	if r.Method == http.MethodHead {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Length", strconv.FormatInt(it.Size, 10))
		h.Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		return
	}
	g.serveBlob(w, r, q.Repo, it)
}

// streamBlob serves a blob's content from the blob store, ranges and
// conditional requests included: the registry's own download, for a
// server that sets no Options.ServeBlob.
func (g *Registry) streamBlob(
	w http.ResponseWriter,
	r *http.Request,
	_ repo.Config,
	it types.Item,
) {
	content, _, err := g.blobs.Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	defer func() { _ = content.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", it.Modified, content)
}

// deleteBlob refuses: a blob lives as long as the manifests that reference
// it, and goes with them (decision 0031). The conformance suite accepts
// 405 for a registry that does not delete blobs.
func deleteBlob(w http.ResponseWriter, _ *http.Request, _ Request) {
	w.Header().Set("Allow", "GET, HEAD")
	Write(w, Errorf(http.StatusMethodNotAllowed, CodeUnsupported,
		"blobs are removed with the manifests that reference them: delete the tag or manifest"))
}

// startUpload serves POST .../blobs/uploads/: a monolithic upload with
// ?digest=, a mount with ?mount=&from= when the caller may read the source
// and it holds the blob, and otherwise a new session.
func (g *Registry) startUpload(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionWrite); err != nil {
		Write(w, err)
		return
	}
	query := r.URL.Query()
	if a := query.Get("digest-algorithm"); a != "" && a != "sha256" {
		Write(w, Errorf(http.StatusBadRequest, CodeDigestInvalid,
			"unsupported digest algorithm %q: only sha256 is supported", a))
		return
	}
	o := owner(r, q)
	if query.Has("digest") {
		hex, err := uploadDigest(query.Get("digest"))
		if err != nil {
			Write(w, err)
			return
		}
		if _, err := g.uploads.Upload(r.Context(), o, r.Body, hex); err != nil {
			g.uploadErr(w, r, q, err)
			return
		}
		created(w, r, q, hex)
		return
	}
	if query.Has("mount") && query.Has("from") && g.mount(w, r, q, o) {
		return
	}
	id, err := g.uploads.Begin(r.Context(), o)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	if ps, ok := g.blobs.(blob.PartSizer); ok {
		// An object store writes in parts of this size and keeps a shorter
		// tail in the session's row until the next chunk; chunks of at
		// least a part keep that row small.
		w.Header().Set(chunkMinHeader, strconv.FormatInt(ps.PartSize(), 10))
	}
	session(w, r, q, id, 0, http.StatusAccepted)
}

// mount answers a cross-repository mount when it can and reports whether
// it wrote a response. false opens a session instead: the source is not
// readable, holds no such blob, or does not parse, and a client learns
// nothing from the difference.
func (g *Registry) mount(w http.ResponseWriter, r *http.Request, q Request, o registry.Owner) bool {
	query := r.URL.Query()
	hex, err := uploadDigest(query.Get("mount"))
	if err != nil {
		return false
	}
	for _, src := range g.mountSources(r, q, query.Get("from")) {
		if src.Require(r, src.Name+"/", types.ActionRead) != nil {
			continue
		}
		_, ok, err := g.uploads.Mount(r.Context(), o, src.Repo, src.Name, hex)
		if err != nil {
			g.uploadErr(w, r, q, err)
			return true
		}
		if ok {
			created(w, r, q, hex)
			return true
		}
	}
	return false
}

// mountSources are the images from may name, as the client addresses
// images (decision 0031). On the repository path it is <repo>/<image>. On
// a subdomain it is an image of the same repository. Under the Artifactory
// prefix it is either, as the client sees the registry through whichever
// method the reverse proxy in front of it rewrites from: the repository
// form is tried first when its first segment names a repository. Each
// source keeps from as the client wrote it and the realm the request's own
// is under, which a registry token's grant for it names (Require).
func (g *Registry) mountSources(r *http.Request, q Request, from string) []Request {
	same := Request{RepoKey: q.RepoKey, Repo: q.Repo, Name: from, base: q.base}
	var out []Request
	if q.base != "/v2" {
		if key, name, ok := strings.Cut(from, "/"); ok {
			c, found, err := g.lookup(r.Context(), key)
			if err == nil && found && c.Class != repo.Remote && c.Class != repo.Virtual {
				src := Request{RepoKey: key, Repo: c, Name: name, base: "/v2/" + key}
				if strings.HasPrefix(q.base, prefix) {
					src = Request{
						RepoKey: q.RepoKey,
						Repo:    c,
						Name:    name,
						base:    q.base,
						scope:   from,
					}
				}
				out = append(out, src)
			}
		}
	}
	if q.base == "/v2" || strings.HasPrefix(q.base, prefix) {
		out = append(out, same)
	}
	valid := out[:0]
	for _, s := range out {
		if ValidName(s.Name) {
			valid = append(valid, s)
		}
	}
	return valid
}

// uploadStatus serves GET on a session: 204 with how much it holds.
func (g *Registry) uploadStatus(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionWrite); err != nil {
		Write(w, err)
		return
	}
	size, err := g.uploads.Status(r.Context(), owner(r, q), q.Reference)
	if err != nil {
		g.uploadErr(w, r, q, err)
		return
	}
	session(w, r, q, q.Reference, size, http.StatusNoContent)
}

// appendUpload serves PATCH on a session: a chunk, which must start where
// the session ends when it carries Content-Range, or a stream without it.
func (g *Registry) appendUpload(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionWrite); err != nil {
		Write(w, err)
		return
	}
	offset, body, err := chunk(r)
	if err != nil {
		Write(w, err)
		return
	}
	size, err := g.uploads.Append(r.Context(), owner(r, q), q.Reference, offset, body)
	if err != nil {
		g.chunkErr(w, r, q, offset, size, err)
		return
	}
	session(w, r, q, q.Reference, size, http.StatusAccepted)
}

// finishUpload serves PUT on a session: the last chunk, if any, and the
// digest the content must have.
func (g *Registry) finishUpload(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionWrite); err != nil {
		Write(w, err)
		return
	}
	query := r.URL.Query()
	if !query.Has("digest") {
		Write(w, Errorf(http.StatusBadRequest, CodeDigestInvalid,
			"the digest parameter is required to complete an upload"))
		return
	}
	hex, err := uploadDigest(query.Get("digest"))
	if err != nil {
		Write(w, err)
		return
	}
	offset, body, err := chunk(r)
	if err != nil {
		Write(w, err)
		return
	}
	o := owner(r, q)
	if _, err := g.uploads.Finish(r.Context(), o, q.Reference, offset, body, hex); err != nil {
		var size int64
		if errors.Is(err, registry.ErrOffset) {
			// The session is untouched; the client resumes where it ends.
			size, _ = g.uploads.Status(r.Context(), o, q.Reference)
		}
		g.chunkErr(w, r, q, offset, size, err)
		return
	}
	created(w, r, q, hex)
}

// cancelUpload serves DELETE on a session.
func (g *Registry) cancelUpload(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionWrite); err != nil {
		Write(w, err)
		return
	}
	if err := g.uploads.Cancel(r.Context(), owner(r, q), q.Reference); err != nil {
		g.uploadErr(w, r, q, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// chunkErr answers a failed append: an out-of-order chunk with 416 and
// where the session ends, so the client resumes there.
func (g *Registry) chunkErr(
	w http.ResponseWriter,
	r *http.Request,
	q Request,
	offset, size int64,
	err error,
) {
	if !errors.Is(err, registry.ErrOffset) {
		g.uploadErr(w, r, q, err)
		return
	}
	h := w.Header()
	h.Set("Location", location(r, q, "blobs", "uploads", q.Reference))
	h.Set("Range", uploadRange(size))
	h.Set(uploadUUIDHeader, q.Reference)
	Write(w, Errorf(http.StatusRequestedRangeNotSatisfiable, CodeBlobUploadInvalid,
		"the chunk starts at byte %d, but the upload holds %d bytes", offset, size))
}

// uploadErr answers what the upload service refused.
func (g *Registry) uploadErr(w http.ResponseWriter, r *http.Request, q Request, err error) {
	switch {
	case errors.Is(err, registry.ErrUnknown):
		Write(w, Errorf(http.StatusNotFound, CodeBlobUploadUnknown,
			"blob upload unknown to registry: %s", q.Reference))
	case errors.Is(err, registry.ErrDigest):
		Write(w, Errorf(http.StatusBadRequest, CodeDigestInvalid,
			"the uploaded content does not match the digest given"))
	case errors.Is(err, item.ErrConflict):
		// A file stands where the image's folders must go.
		Write(w, Errorf(http.StatusBadRequest, CodeNameInvalid,
			"%s cannot hold an image: a file is in the way", q.Name))
	default:
		g.writeErr(w, r, err)
	}
}

// created answers a stored blob: 201 with its location and digest.
func created(w http.ResponseWriter, r *http.Request, q Request, hex string) {
	h := w.Header()
	h.Set("Location", location(r, q, "blobs", "sha256:"+hex))
	h.Set(contentDigestHeader, "sha256:"+hex)
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// session answers with a session's location and how much it holds.
func session(w http.ResponseWriter, r *http.Request, q Request, id string, size int64, status int) {
	h := w.Header()
	h.Set("Location", location(r, q, "blobs", "uploads", id))
	h.Set("Range", uploadRange(size))
	h.Set(uploadUUIDHeader, id)
	if status != http.StatusNoContent {
		h.Set("Content-Length", "0")
	}
	w.WriteHeader(status)
}

// uploadRange is a session's Range header: the inclusive range of bytes it
// holds, 0-0 when empty, as the reference registry sends.
func uploadRange(size int64) string {
	return "0-" + strconv.FormatInt(max(size-1, 0), 10)
}

// location is the absolute URL of an operation on q's image in the form
// the client addressed (decision 0031). The base URL resolver names the
// host of every method but the subdomain, whose /v2 sits at the root of the
// repository's own host: <repo>.<the base URL's host>, unless the base URL
// already names it, as an X-JFrog-Override-Base-Url built from the
// subdomain's server name does. The host always comes from the resolved
// base URL, never from a header the request carries, so a configured
// base_url decides it (#313). Without a resolved base URL, as in tests of
// the router alone, the path is relative, which clients accept too.
func location(r *http.Request, q Request, segments ...string) string {
	p := q.Path(segments...)
	base := baseurl.From(r.Context())
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || q.base != "/v2" {
		return base + p
	}
	host := u.Host
	if !strings.HasPrefix(strings.ToLower(u.Hostname()), strings.ToLower(q.RepoKey)+".") {
		host = q.RepoKey + "." + host
	}
	return u.Scheme + "://" + host + p
}

// owner is the session owner of a request: its repository, its image and
// the caller.
func owner(r *http.Request, q Request) registry.Owner {
	p, _ := auth.FromContext(r.Context())
	return registry.Owner{Repo: q.Repo, Name: q.Name, By: p.Name}
}

// digestRE is the specification's digest grammar: an algorithm and its
// encoded value.
var (
	digestRE = regexp.MustCompile(`^[a-z0-9]+(?:[+._-][a-z0-9]+)*:[a-zA-Z0-9=_-]+$`)
	sha256RE = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// parseDigest splits a digest. It refuses with DIGEST_INVALID one that
// does not parse, or a sha256 digest whose value is not 64 lowercase hex
// characters; hex is empty for another algorithm.
func parseDigest(d string) (hex string, err error) {
	if !digestRE.MatchString(d) {
		return "", Errorf(http.StatusBadRequest, CodeDigestInvalid, "invalid digest %q", d)
	}
	alg, enc, _ := strings.Cut(d, ":")
	if alg != "sha256" {
		return "", nil
	}
	if !sha256RE.MatchString(enc) {
		return "", Errorf(http.StatusBadRequest, CodeDigestInvalid, "invalid digest %q", d)
	}
	return enc, nil
}

// blobDigest is the SHA-256 of the blob a read names. A blob under another
// algorithm cannot be stored, so it is unknown.
func blobDigest(d string) (string, error) {
	hex, err := parseDigest(d)
	if err == nil && hex == "" {
		err = blobUnknown(d)
	}
	return hex, err
}

// uploadDigest is the SHA-256 an upload declares; only sha256 is accepted
// (decision 0031).
func uploadDigest(d string) (string, error) {
	hex, err := parseDigest(d)
	if err == nil && hex == "" {
		err = Errorf(http.StatusBadRequest, CodeDigestInvalid,
			"unsupported digest %q: only sha256 is supported", d)
	}
	return hex, err
}

// rangeRE is a chunk's Content-Range: the specification's <start>-<end>,
// inclusive, with HTTP's "bytes " unit and "/<length>" accepted too.
var rangeRE = regexp.MustCompile(`^(?:bytes )?([0-9]+)-([0-9]+)(?:/(?:[0-9]+|\*))?$`)

// chunk reads a chunk's Content-Range: where it starts, negative without
// one, and its body, cut to the range's length. A range that does not
// parse answers 416 BLOB_UPLOAD_INVALID, and a Content-Length that
// disagrees with it 400 SIZE_INVALID.
func chunk(r *http.Request) (int64, io.Reader, error) {
	cr := r.Header.Get("Content-Range")
	if cr == "" {
		return -1, r.Body, nil
	}
	m := rangeRE.FindStringSubmatch(strings.TrimSpace(cr))
	var start, end int64
	var err error
	if m != nil {
		start, err = strconv.ParseInt(m[1], 10, 64)
		if err == nil {
			end, err = strconv.ParseInt(m[2], 10, 64)
		}
	}
	if m == nil || err != nil || end < start {
		return 0, nil, Errorf(http.StatusRequestedRangeNotSatisfiable, CodeBlobUploadInvalid,
			"invalid Content-Range %q", cr)
	}
	length := end - start + 1
	if r.ContentLength >= 0 && r.ContentLength != length {
		return 0, nil, Errorf(http.StatusBadRequest, CodeSizeInvalid,
			"Content-Length %d does not match Content-Range %q", r.ContentLength, cr)
	}
	return start, io.LimitReader(r.Body, length), nil
}
