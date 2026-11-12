package itemkit

import (
	"io"
	"net/http"
	"strings"
)

// bareETagWriter sends the quoted ETag "<sha1>" in its header map without
// the quotes, and quoteETag makes a request that sends the bare form back
// match it.
//
// A file's ETag is its bare SHA-1, as 7.x sends it on the item route and on
// the protocol routes that serve a stored file: unquoted and equal to
// X-Checksum-Sha1 on a Maven download from 7.94.1 (RTFACT-30785), a HEAD
// from 7.55.8 (RTFACT-29977), a 206 from PyPI's route on 7.77.6
// (RTFACT-30389) and the Vagrant box file route on 7.37.15 (RTFACT-27264).
// JFrog closed RTFACT-19373, which asked for RFC 9110's quotes, without
// adding them.
//
// http.ServeContent parses only the quoted form, so answerPreconditions and
// ServeContent keep comparing against the quoted ETag in the header map,
// and the quotes come off only as the status is written. Every way a status
// reaches the wire passes through unquote first: ServeContent writes the
// status before any byte, and an error body, such as a 416's, through
// Write.
type bareETagWriter struct {
	http.ResponseWriter
	sha1 string
}

// unquote takes the quotes off the ETag. A 302 or a 416 has had its ETag
// removed by then, and is left as it is.
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

// ReadFrom keeps the underlying writer's zero-copy path, as StatusWriter's
// does.
func (b *bareETagWriter) ReadFrom(r io.Reader) (int64, error) {
	b.unquote()
	return io.Copy(b.ResponseWriter, r) //nolint:wrapcheck // a pass-through writer
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (b *bareETagWriter) Unwrap() http.ResponseWriter { return b.ResponseWriter }

// quoteETag returns r with sha1 quoted in If-None-Match, alone or in a list
// and with or without a weak prefix, as decision 0034 matches an npm
// packument's bare ETag; and in If-Range, where only the strong form can
// match. Without it, a client sending back the ETag it was given would get
// the whole file again instead of a 304 or the range it asked for. Every
// other value reaches ServeContent as it came, so only a request naming the
// file's bare SHA-1 is answered differently. The copy leaves the caller's
// request as it was sent.
func quoteETag(r *http.Request, sha1 string) *http.Request {
	noneMatch, ifRange := r.Header.Get("If-None-Match"), r.Header.Get("If-Range")
	entries := strings.Split(noneMatch, ",")
	for i, entry := range entries {
		tag := strings.TrimSpace(entry)
		if strings.TrimPrefix(tag, "W/") == sha1 {
			entries[i] = strings.TrimSuffix(tag, sha1) + `"` + sha1 + `"`
		}
	}
	quotedNoneMatch := strings.Join(entries, ",")
	quotedIfRange := ifRange
	if strings.TrimSpace(ifRange) == sha1 {
		quotedIfRange = `"` + sha1 + `"`
	}
	if quotedNoneMatch == noneMatch && quotedIfRange == ifRange {
		return r
	}
	out := r.WithContext(r.Context())
	out.Header = r.Header.Clone()
	if quotedNoneMatch != noneMatch {
		out.Header.Set("If-None-Match", quotedNoneMatch)
	}
	if quotedIfRange != ifRange {
		out.Header.Set("If-Range", quotedIfRange)
	}
	return out
}
