package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/auth"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	if err := q.Require(r, q.Name+"/", types.ActionRead); err != nil {
		Write(w, err)
		return
	}
	op := map[Kind]string{KindManifest: "manifests", KindBlob: "blobs", KindTags: "tags", KindReferrers: "referrers"}[q.Kind]
	ref := q.Reference
	if q.Kind == KindTags {
		ref = "list"
	}
	resource, err := remote.RegistryResource(q.Name, op, ref)
	if err != nil {
		Write(w, Errorf(http.StatusBadRequest, CodeNameInvalid, "invalid registry reference"))
		return
	}
	p, _ := auth.FromContext(r.Context())
	result, err := g.sources.Read(
		r.Context(),
		source.Request{
			Repo:     q.Repo,
			Subject:  p.Subject(),
			Access:   p.Access(),
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
	if errors.Is(err, store.ErrNotFound) {
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
	p, _ := auth.FromContext(r.Context())
	err = pkgroute.SourceError(err, p.Anonymous || p.Name == "")
	var e *apierror.Error
	if errors.As(err, &e) {
		if e.Code == "" {
			e.Code = CodeDenied
			if e.Status == 429 {
				e.Code = CodeTooManyRequests
			}
		}
	}
	g.writeErr(w, r, err)
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
