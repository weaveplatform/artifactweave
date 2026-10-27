package pkgroute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Remote opts one protocol into the common source boundary. It is never
// installed by default; each adapter must first support remote metadata and
// transient results. Docker keeps its separate routing seam.
type Remote struct{ Service *source.Service }

// Item cannot represent transient content. A protocol using Remote must consume
// Read instead of assuming that every successful download has a stored item.
func (Remote) Item(context.Context, store.Store, repo.Config, string) (Found, error) {
	return Found{}, source.ErrUnsupported
}

// Read resolves artifact, metadata or version content through the common policy.
// A native adapter supplies its normalized resource and escaped upstream path;
// caller credentials are never copied into that resource.
func (s Remote) Read(
	r *http.Request,
	c repo.Config,
	resource source.Resource,
) (source.Result, error) {
	if s.Service == nil {
		return source.Result{}, apierror.ErrNotImplemented
	}
	p, _ := auth.FromContext(r.Context())
	result, err := s.Service.Read(r.Context(), source.Request{
		Repo:      c,
		CacheOnly: RequestFrom(r).CacheOnly,
		Subject:   p.Subject(),
		Access:    p.Access(),
		Resource:  resource,
		Project:   c.ProjectKey,
	})
	return result, SourceError(err, p.Anonymous || p.Name == "")
}

// ReadSource is the optional stream-aware extension of a protocol Source.
// Format packages consume internal/source directly and never import this HTTP
// adapter interface.
type ReadSource interface {
	Read(*http.Request, repo.Config, source.Resource) (source.Result, error)
}

// Read is the stream-aware extension of Item. A future virtual source can
// implement the same optional method while retaining each member's permission
// and fetch intent. Existing local sources continue using their stored items.
func (k *Kit) Read(r *http.Request, resource source.Resource) (source.Result, error) {
	q := RequestFrom(r)
	allowed := can(r, q.Repo, resource.Path, types.ActionRead)
	if resource.PermissionFolder {
		allowed = k.CanFolder(r, resource.PermissionPath, types.ActionRead)
	}
	if !allowed {
		return source.Result{}, refusal(r)
	}
	if src, ok := q.Source.(ReadSource); ok {
		result, err := src.Read(r, q.Repo, resource)
		if err != nil {
			return source.Result{}, fmt.Errorf("read source: %w", err)
		}
		return result, nil
	}
	if resource.Kind != source.ResourceKindArtifact {
		return source.Result{}, source.ErrUnsupported
	}
	f, err := k.Item(r, resource.Path)
	if err != nil {
		return source.Result{}, err
	}
	return source.Result{Item: &f.Item}, nil
}

// Resource builds an artifact request with an explicit escaped upstream path.
// Conditions belong to the downstream representation: a policy backend must
// not use these as upstream validators without a matching stored representation.
func Resource(r *http.Request, path, escaped string) source.Resource {
	return source.Resource{
		Kind:        source.ResourceKindArtifact,
		Path:        path,
		EscapedPath: escaped,
		Method:      r.Method,
		Query:       r.URL.RawQuery,
		Accept:      r.Header.Get("Accept"),
		Range:       r.Header.Get("Range"),
		IfRange:     r.Header.Get("If-Range"),
		IfNoneMatch: r.Header.Get(
			"If-None-Match",
		),
		IfModifiedSince: r.Header.Get("If-Modified-Since"),
	}
}

// SourceError maps only known source errors. Other errors retain the router's
// existing internal-fault treatment; no upstream body or challenge is relayed.
func SourceError(err error, anonymous bool) error {
	var failure *source.HTTPError
	var rejected *format.RejectionError
	var status int
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rejected):
		return apierror.New(rejected.Status, rejected.Message)
	case errors.As(err, &failure):
		status = failure.Status
		e := apierror.New(status, http.StatusText(status))
		e.NoChallenge = true
		if failure.RetryAfter > 0 && failure.RetryAfter <= 5*time.Minute {
			e.RetryAfterSeconds = int((failure.RetryAfter + time.Second - 1) / time.Second)
		}
		return e
	case errors.Is(err, source.ErrDenied):
		if anonymous {
			return auth.ErrUnauthenticated
		}
		return auth.ErrForbidden
	case errors.Is(err, source.ErrInvalidRequest):
		status = http.StatusBadRequest
	case errors.Is(err, source.ErrUnsupported):
		status = http.StatusNotImplemented
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case errors.Is(err, source.ErrSuperseded):
		status = http.StatusServiceUnavailable
	case errors.Is(err, source.ErrUnavailable),
		errors.Is(err, source.ErrInvalidResult),
		errors.Is(err, context.Canceled):
		status = http.StatusBadGateway
	default:
		return err
	}
	return apierror.New(status, http.StatusText(status))
}

// ServeStream owns and closes the body, including HEAD/304. It writes only
// representation headers, never cookies or upstream authentication challenges.
// An error after headers requires the caller to abort the HTTP stream: a partial
// unknown-length body must not look like a successfully completed artifact.
func ServeStream(w http.ResponseWriter, r *http.Request, s *source.Stream) error {
	defer func() { _ = s.Body.Close() }()
	h := w.Header()
	if s.MediaType != "" {
		h.Set("Content-Type", s.MediaType)
	}
	if s.ETag != "" {
		h.Set("ETag", s.ETag)
	}
	if !s.Modified.IsZero() {
		h.Set("Last-Modified", s.Modified.UTC().Format(http.TimeFormat))
	}
	if s.ContentRange != "" {
		h.Set("Content-Range", s.ContentRange)
	}
	if s.Size >= 0 && s.Status != http.StatusNotModified {
		h.Set("Content-Length", strconv.FormatInt(s.Size, 10))
	}
	SetChecksums(h, s.Checksums)
	// Validated temporary files and raw cached metadata are seekable. ServeContent
	// evaluates downstream conditions and ranges against that complete response.
	if seeker, ok := s.Body.(io.ReadSeeker); ok && s.Status == http.StatusOK {
		checked := &checkedContent{ReadSeeker: seeker}
		tracked := &streamWriter{ResponseWriter: w}
		http.ServeContent(tracked, r, "", s.Modified, checked)
		return errors.Join(checked.err, tracked.err)
	}
	if r.Method == http.MethodHead && s.Status == http.StatusOK && s.Size >= 0 {
		http.ServeContent(w, r, "", s.Modified, headContent{size: s.Size})
		return nil
	}
	w.WriteHeader(s.Status)
	if r.Method == http.MethodHead || s.Status == http.StatusNotModified {
		return nil
	}
	n, err := io.Copy(w, s.Body)
	if err != nil || (s.Size >= 0 && n != s.Size) {
		return source.ErrUnavailable
	}
	return nil
}

// checkedContent exposes a read failure which ServeContent cannot return.
type checkedContent struct {
	io.ReadSeeker
	err error
}

func (c *checkedContent) Read(p []byte) (int, error) {
	n, err := c.ReadSeeker.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		c.err = source.ErrUnavailable
	}
	return n, err //nolint:wrapcheck // the original stream error
}

// A HEAD has no body but still needs standard conditional response handling.
type headContent struct{ size int64 }

func (h headContent) Read([]byte) (int, error) { return 0, io.EOF }
func (h headContent) Seek(_ int64, whence int) (int64, error) {
	if whence == io.SeekEnd {
		return h.size, nil
	}
	return 0, nil
}

// EscapedResourcePath drops the route prefix without decoding package slashes.
// Matrix parameters describe local selection, not an upstream path segment.
func EscapedResourcePath(r *http.Request, prefix string) string {
	path := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i], _, _ = strings.Cut(seg, ";")
	}
	return strings.Join(segments, "/")
}

func escapeResource(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// ServeContent has no error result; retain client write failures for the caller.
type streamWriter struct {
	http.ResponseWriter
	err error
}

func (w *streamWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		w.err = source.ErrUnavailable
	}
	return n, err //nolint:wrapcheck // preserves the network writer's error
}
func (w *streamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeResult delivers an already resolved representation after its final
// authority check. Callers may rewrite metadata first, preserving Revalidate
// and Used while replacing every header that describes the upstream bytes.
func (k *Kit) ServeResult(w http.ResponseWriter, r *http.Request, result source.Result) {
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
			}
			p, _ := auth.FromContext(r.Context())
			k.Error(w, r, SourceError(err, p.Anonymous))
			return
		}
	}
	tracked := &deliveryWriter{ResponseWriter: w}
	if result.Stream != nil {
		if err := ServeStream(tracked, r, result.Stream); err != nil {
			if k.o.Fault != nil {
				k.o.Fault(r, err)
			}
			panic(http.ErrAbortHandler)
		}
	} else {
		k.o.ServeItem(tracked, r, RequestFrom(r).Repo, *result.Item)
	}
	if tracked.complete(r) && result.Used != nil {
		_ = result.Used(r.Context())
	}
}

type deliveryWriter struct {
	http.ResponseWriter
	status    int
	bytes     int64
	hasFailed bool
}

func (w *deliveryWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *deliveryWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	w.hasFailed = w.hasFailed || err != nil || n != len(b)
	return n, err //nolint:wrapcheck // pass-through network writer
}
func (w *deliveryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *deliveryWriter) complete(r *http.Request) bool {
	if r.Method != http.MethodGet || w.hasFailed || (w.status != 200 && w.status != 206) {
		return false
	}
	if length := w.Header().Get("Content-Length"); length != "" {
		n, err := strconv.ParseInt(length, 10, 64)
		return err == nil && n == w.bytes
	}
	return true
}
