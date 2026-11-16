package middleware

import (
	"context"
	"io"
	"maps"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// HeadAsGet answers a HEAD through the GET route of its path, for the GET
// routes Serves accepts, where no route claims the HEAD itself. 7.x serves
// its REST API with Jersey, which hands a HEAD to the resource's @HEAD
// method or, when there is none, to its @GET method and leaves the entity
// out (JAX-RS 2.1, section 3.3.5). chi routes a HEAD only to a route that
// registers HEAD, so without this a HEAD of a GET-only operation answers
// 405.
//
// The router installs it in two places. Enter decides, and from there on
// the request is its GET: the key gate, the recovery mark, admission,
// authentication's public routes and the restriction check all read the
// method, and each must decide a HEAD as it decides the GET, or the HEAD
// would answer a status or rate header the GET does not. Route, the last
// middleware before routing, gives the handler its HEAD back, because a
// file download reads it: a HEAD ignores Range and is no delivery of the
// file (the VCS downloads).
type HeadAsGet struct {
	// Serves reports whether the GET route with this chi pattern answers a
	// HEAD too. It is asked "" for a path no GET route serves.
	Serves func(pattern string) bool
}

// headAsGetKey marks a HEAD that Enter turned into its GET.
type headAsGetKey struct{}

// Enter turns a HEAD whose path no HEAD route serves, and whose GET route
// Serves accepts, into that GET for the middleware after it, and answers
// it without a body. It looks the routes up on the path as it stands, so it
// must follow every middleware that rewrites the path. Any other request
// passes as it was, so a HEAD that a route of its own serves keeps that
// route, and one with no GET route keeps its 404 or 405.
func (h HeadAsGet) Enter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || !h.servesGet(r) {
			next.ServeHTTP(w, r)
			return
		}
		hw := &headWriter{ResponseWriter: w}
		get := r.WithContext(context.WithValue(r.Context(), headAsGetKey{}, true))
		get.Method = http.MethodGet
		next.ServeHTTP(hw, get)
		hw.finish()
	})
}

// Route routes the request Enter turned into a GET on its GET route, as
// the HEAD it is.
func (HeadAsGet) Route(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.RouteContext(r.Context())
		if r.Context().Value(headAsGetKey{}) == nil || rctx == nil {
			next.ServeHTTP(w, r)
			return
		}
		rctx.RouteMethod = http.MethodGet
		head := r.WithContext(r.Context())
		head.Method = http.MethodHead
		next.ServeHTTP(w, head)
	})
}

// servesGet reports whether r's path, as chi will route r on it, has a GET
// route that h.Serves accepts and no HEAD route that matches the path as
// closely.
//
// JAX-RS picks the resource path first and the method after it (section
// 3.7.2): of the templates that match, the one with the most literal
// characters wins, and a HEAD then goes to its @HEAD, or else its @GET.
// chi picks the method first, and backtracks into a parameter to find a
// HEAD route, so HEAD /distribution/api/v1/release_bundle/b/LATEST would
// reach the HEAD of .../{release_bundle_name}/{release_bundle_version}
// rather than the GET of .../{release_bundle_name}/LATEST. A HEAD route
// with fewer literal characters than the GET route is that backtrack; on
// a tie, the HEAD route keeps the request, as it does when both are the
// same template.
func (h HeadAsGet) servesGet(r *http.Request) bool {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil || rctx.Routes == nil {
		return false
	}
	path := rctx.RoutePath
	if path == "" {
		path = r.URL.RawPath
	}
	if path == "" {
		path = r.URL.Path
	}
	get := rctx.Routes.Find(chi.NewRouteContext(), http.MethodGet, path)
	if !h.Serves(get) {
		return false
	}
	head := rctx.Routes.Find(chi.NewRouteContext(), http.MethodHead, path)
	return head == "" || literals(get) > literals(head)
}

// literals counts the characters of a chi pattern outside its parameters
// and wildcard, JAX-RS's first key for ordering path templates.
func literals(pattern string) int {
	n, depth := 0, 0
	for _, c := range pattern {
		switch {
		case c == '{':
			depth++
		case c == '}':
			depth--
		case depth == 0 && c != '*':
			n++
		}
	}
	return n
}

// sniffLen is how much of a body net/http reads to detect its type
// (http.DetectContentType).
const sniffLen = 512

// headWriter answers a HEAD with the status and header fields its GET
// would send, and no body. It holds the header until the handler returns,
// so that it can send the Content-Length of the body it drops, which
// RFC 9110 section 8.6 allows on a HEAD when it is the GET's length:
// net/http cannot, as it sees no body. As net/http does for a GET, it
// takes the header as it stood when the handler began its answer, and
// sniffs the type of a body whose handler set none.
type headWriter struct {
	http.ResponseWriter

	status int
	header http.Header
	sniff  []byte
	n      int64
}

// WriteHeader records the status and the header as they stand.
func (w *headWriter) WriteHeader(status int) {
	if w.header != nil {
		return
	}
	w.status = status
	w.header = w.ResponseWriter.Header().Clone()
}

// Write counts p and drops it.
func (w *headWriter) Write(p []byte) (int, error) {
	if w.header == nil {
		w.WriteHeader(http.StatusOK)
	}
	if room := sniffLen - len(w.sniff); room > 0 {
		w.sniff = append(w.sniff, p[:min(room, len(p))]...)
	}
	w.n += int64(len(p))
	return len(p), nil
}

// ReadFrom drops what a handler copies from src without reading it when
// the handler declared its length, so that a HEAD of a file download does
// not read the file; io.Copy prefers it to Write. A body of undeclared
// length is read to count it.
func (w *headWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.header == nil {
		w.WriteHeader(http.StatusOK)
	}
	if w.header.Get("Content-Length") != "" {
		return 0, nil
	}
	n, err := io.Copy(struct{ io.Writer }{w}, src)
	return n, err //nolint:wrapcheck // the handler's own reader's error
}

// Flush does nothing: the header waits for the handler to return.
func (*headWriter) Flush() {}

// Unwrap is the writer it wraps, for http.ResponseController.
func (w *headWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish sends the header: the status the handler wrote (200 when it wrote
// none, as net/http answers), the fields as they stood then, and the type
// and length of the dropped body where net/http would have set them for
// the GET.
func (w *headWriter) finish() {
	if w.header == nil {
		w.WriteHeader(http.StatusOK)
	}
	h := w.Header()
	for name := range h {
		if _, kept := w.header[name]; !kept {
			delete(h, name)
		}
	}
	maps.Copy(h, w.header)
	if bodyAllowed(w.status) && h.Get("Transfer-Encoding") == "" {
		if _, typed := h["Content-Type"]; !typed && len(w.sniff) > 0 {
			h.Set("Content-Type", http.DetectContentType(w.sniff))
		}
		if h.Get("Content-Length") == "" {
			h.Set("Content-Length", strconv.FormatInt(w.n, 10))
		}
	}
	w.ResponseWriter.WriteHeader(w.status)
}

// bodyAllowed reports whether a response with status may have a body
// (RFC 9110 sections 15.2, 15.3.5 and 15.4.5), and so a Content-Length.
func bodyAllowed(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent &&
		status != http.StatusNotModified
}
