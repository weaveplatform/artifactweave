package pkgroute

import (
	"io"
	"net/http"
	"strings"
)

// bareETag gives a file stream the ETag a kept copy answers with, its bare
// SHA-1, as the item kit's BareETag gives a stored file served outside
// ServeFile; the kit cannot import the item kit, which imports it. It sets
// the quoted "<sha1>" that http.ServeContent compares as w's ETag, and
// returns w wrapped to send it bare, and r with an If-Range naming the bare
// SHA-1 quoted so that ServeContent matches it. sha1 must not be empty.
func bareETag(
	w http.ResponseWriter,
	r *http.Request,
	sha1 string,
) (http.ResponseWriter, *http.Request) {
	w.Header().Set("ETag", `"`+sha1+`"`)
	if strings.TrimSpace(r.Header.Get("If-Range")) == sha1 {
		out := r.WithContext(r.Context())
		out.Header = r.Header.Clone()
		out.Header.Set("If-Range", `"`+sha1+`"`)
		r = out
	}
	return &bareETagWriter{ResponseWriter: w, sha1: sha1}, r
}

// bareETagWriter takes the quotes off the ETag "<sha1>" as the status goes
// out: the header map keeps the quoted form, the only one ServeContent
// parses, until then. A 416 has had its ETag removed by then and is left
// as it is.
type bareETagWriter struct {
	http.ResponseWriter
	sha1 string
}

func (b *bareETagWriter) unquote() {
	if h := b.Header(); h.Get("ETag") == `"`+b.sha1+`"` {
		h.Set("ETag", b.sha1)
	}
}

// WriteHeader sends the ETag bare with the status.
func (b *bareETagWriter) WriteHeader(code int) {
	b.unquote()
	b.ResponseWriter.WriteHeader(code)
}

// Write sends the ETag bare when the status goes out with these bytes.
func (b *bareETagWriter) Write(p []byte) (int, error) {
	b.unquote()
	return b.ResponseWriter.Write(p) //nolint:wrapcheck // a pass-through writer
}

// ReadFrom keeps the underlying writer's zero-copy path.
func (b *bareETagWriter) ReadFrom(r io.Reader) (int64, error) {
	b.unquote()
	return io.Copy(b.ResponseWriter, r) //nolint:wrapcheck // a pass-through writer
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (b *bareETagWriter) Unwrap() http.ResponseWriter { return b.ResponseWriter }
