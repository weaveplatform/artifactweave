package dockerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (g *Registry) serveRemote(w http.ResponseWriter, r *http.Request, q Request) {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
		(q.Kind != KindManifest && q.Kind != KindBlob && q.Kind != KindTags && q.Kind != KindReferrers) {
		Write(
			w,
			Errorf(
				http.StatusMethodNotAllowed,
				CodeUnsupported,
				"remote registries support content reads only",
			),
		)
		return
	}
	if err := q.Require(r, q.Name+"/", accessapi.ActionRead); err != nil {
		Write(w, err)
		return
	}
	op := map[Kind]string{KindManifest: "manifests", KindBlob: "blobs", KindTags: "tags", KindReferrers: "referrers"}[q.Kind]
	ref := q.Reference
	if q.Kind == KindTags {
		ref = "list"
	}
	resource, err := layout.RegistryResource(q.Name, op, ref)
	if err != nil {
		Write(w, Errorf(http.StatusBadRequest, CodeNameInvalid, "invalid registry reference"))
		return
	}
	p, _ := kernel.FromContext(r.Context())
	d, _ := accessapi.DeciderFrom(r.Context())
	result, err := g.sources.Read(
		r.Context(),
		source.Request{
			Repo:     q.Repo,
			Subject:  accessapi.SubjectOf(p),
			Access:   d,
			Resource: resource,
			Project:  q.Repo.ProjectKey,
		},
	)
	if err != nil {
		g.remoteError(w, r, q, err)
		return
	}
	stream, err := g.remoteStream(r, result)
	if err != nil {
		g.remoteError(w, r, q, err)
		return
	}
	defer stream.Body.Close()
	if result.Revalidate != nil {
		if err = result.Revalidate(r.Context()); err != nil {
			g.remoteError(w, r, q, err)
			return
		}
	}
	if q.Kind == KindTags || q.Kind == KindReferrers {
		g.remoteListing(w, r, q, result, stream)
		return
	}
	if q.Kind == KindManifest && !accepts(r.Header.Values("Accept"), stream.MediaType) {
		Write(w, manifestUnknown(q))
		return
	}
	digest := "sha256:" + stream.Checksums.SHA256
	w.Header().Set(contentDigestHeader, digest)
	w.Header().Set("ETag", `"`+digest+`"`)
	w.Header().Set(RegistryHeader, q.Repo.Key)
	stream.ETag = `"` + digest + `"`
	if q.Kind == KindBlob {
		stream.MediaType = "application/octet-stream"
	}
	tracked := &remoteWriter{ResponseWriter: w}
	if err = pkgroute.ServeStream(tracked, r, stream); err != nil {
		panic(http.ErrAbortHandler)
	}
	if result.Used != nil && r.Method == http.MethodGet &&
		(tracked.status == 200 || tracked.status == 206) {
		_ = result.Used(r.Context())
	}
}

func (g *Registry) remoteStream(r *http.Request, result source.Result) (*source.Stream, error) {
	if result.Stream != nil {
		return result.Stream, nil
	}
	it := result.Item
	body, _, err := g.blobs.Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("open registry cache: %w", err)
	}
	typeName := "application/octet-stream"
	if it.Name == layout.ManifestFile || it.Name == layout.ListManifestFile {
		typeName, err = g.manifestType(r, *it, body)
		if err != nil {
			_ = body.Close()
			return nil, err
		}
	}
	return &source.Stream{
		Body:   body,
		Status: http.StatusOK,
		Representation: source.Representation{
			Checksums: it.Actual,
			Size:      it.Size,
			MediaType: typeName,
			Modified:  it.Modified,
		},
	}, nil
}

func (g *Registry) remoteListing(
	w http.ResponseWriter,
	r *http.Request,
	q Request,
	result source.Result,
	stream *source.Stream,
) {
	var body []byte
	var paths []string
	header := make(http.Header)
	if q.Kind == KindTags {
		var listing struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(io.LimitReader(stream.Body, 16<<20)).
			Decode(&listing); err != nil {
			g.remoteError(w, r, q, source.ErrInvalidResult)
			return
		}
		tags := make([]string, 0, len(listing.Tags))
		for _, tag := range listing.Tags {
			if result.CheckRead == nil ||
				result.CheckRead(r.Context(), q.Name+"/"+tag+"/"+layout.ManifestFile) == nil {
				tags = append(tags, tag)
			}
		}
		slices.SortFunc(tags, layout.Compare)
		page := paginateHeaders(header, r, slices.Compact(tags))
		for _, tag := range page {
			paths = append(paths, q.Name+"/"+tag+"/"+layout.ManifestFile)
		}
		body, _ = json.Marshal(map[string]any{"name": q.addressed(), "tags": page})
		header.Set("Content-Type", "application/json")
	} else {
		var listing referrersIndex
		if err := json.NewDecoder(io.LimitReader(stream.Body, layout.MaxManifestSize)).
			Decode(&listing); err != nil {
			g.remoteError(w, r, q, source.ErrInvalidResult)
			return
		}
		kept := make([]layout.Descriptor, 0, len(listing.Manifests))
		filter := r.URL.Query().Get("artifactType")
		for _, d := range listing.Manifests {
			h, _ := layout.SHA256Hex(d.Digest)
			if (filter == "" || d.ArtifactType == filter) &&
				(result.CheckRead == nil || result.CheckRead(r.Context(), q.Name+"/"+layout.DigestFolder(h)+"/"+layout.ManifestFile) == nil) {
				kept = append(kept, d)
				paths = append(paths, q.Name+"/"+layout.DigestFolder(h)+"/"+layout.ManifestFile)
			}
		}
		listing.Manifests = kept
		body, _ = json.Marshal(listing)
		header.Set("Content-Type", layout.MediaTypeOCIIndex)
		if filter != "" {
			header.Set(filtersHeader, "artifactType")
		}
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			g.remoteError(w, r, q, err)
			return
		}
	}
	for _, path := range paths {
		if result.CheckRead != nil {
			if err := result.CheckRead(r.Context(), path); err != nil {
				g.remoteError(w, r, q, err)
				return
			}
		}
	}
	for key, values := range header {
		w.Header()[key] = values
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := w.Write(body); err == nil && result.Used != nil {
		_ = result.Used(r.Context())
	}
}

func (g *Registry) remoteError(w http.ResponseWriter, r *http.Request, q Request, err error) {
	// The remote repository's own blackout or patterns, which the source
	// boundary checks before any upstream request, answer as a local
	// repository's refusal does (refuse), for its tags and referrers too.
	if refuse(w, err) {
		return
	}
	if errors.Is(err, kernel.ErrNotFound) {
		switch q.Kind {
		case KindBlob:
			Write(w, blobUnknown(q.Reference))
		case KindManifest:
			Write(w, manifestUnknown(q))
		default:
			Write(w, nameUnknown(q))
		}
		return
	}
	p, _ := kernel.FromContext(r.Context())
	answer := pkgroute.SourceError(err, p.Anonymous || p.Name == "")
	if retrievalFailed(err) {
		// The remote's failure to fetch, unlike the caller's refusal, answers
		// in Artifactory's envelope under no registry code, keeping its
		// status, message and Retry-After. docker pull printed "unknown:
		// Invalid username/password configured for Remote Docker repository:
		// ..." when 7.x's upstream refused its credentials (a KB article of
		// 2022, and RTFACT-30467, filed in 2024), and the client prints
		// unknown only for a code it does not know or for none, as with the
		// download policy's refusal (refuse). The 403, 429 and 5xx follow
		// the 401 [I].
		apierror.Write(w, apierror.ProfileArtifactory, answer)
		return
	}
	g.writeErr(w, r, answer)
}

// retrievalFailed reports whether err, which SourceError maps, is the remote
// failing to fetch what was asked: its upstream's answer, which the source
// boundary relays only for a 401, 403, 429 or 5xx, no usable answer, none in
// time, a configuration change during the fetch, or a fetch cut short. The
// caller's own refusal (source.ErrDenied), an invalid source request and an
// unsupported operation keep their registry codes. The checks run in
// SourceError's order, so an error that matches two of them is classed by
// the answer it gets.
func retrievalFailed(err error) bool {
	if errors.As(err, new(*source.HTTPError)) {
		return true
	}
	if errors.Is(err, source.ErrDenied) || errors.Is(err, source.ErrInvalidRequest) ||
		errors.Is(err, source.ErrUnsupported) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, source.ErrSuperseded) ||
		errors.Is(err, source.ErrUnavailable) || errors.Is(err, source.ErrInvalidResult) ||
		errors.Is(err, context.Canceled)
}

type remoteWriter struct {
	http.ResponseWriter
	status int
}

func (w *remoteWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *remoteWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		return n, fmt.Errorf("write registry response: %w", err)
	}
	return n, nil
}
func (w *remoteWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
