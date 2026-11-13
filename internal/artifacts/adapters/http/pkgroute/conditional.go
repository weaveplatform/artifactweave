package pkgroute

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// UnevaluatedPreconditions are the conditional headers Artifactory's
// download never reads. 7.x decides a download's conditional answer in
// DownloadServiceImpl.isNotModified, from If-Modified-Since and
// If-None-Match alone, before it serves a local, cached or remote file or
// its checksum, GET or HEAD, and never answers 412 (DownloadServiceImpl
// respond and isNotModified, Artifactory OSS 7.161.26 sources; 6.20 has
// the same). No class in those sources reads If-Match or
// If-Unmodified-Since. JFrog closed RTFACT-17259, which asked for both, as
// "No plans to implement in near future". 7.125.6 put If-None-Match before
// If-Modified-Since, as RFC 9110 orders them (RTDEV-64461), and named no
// other precondition.
var UnevaluatedPreconditions = []string{"If-Match", "If-Unmodified-Since"}

// WithoutUnevaluatedPreconditions is r without UnevaluatedPreconditions, so
// that a download answers neither with 412, whatever tag or date they
// carry: http.ServeContent applies every RFC 9110 precondition, so a client
// sending back the bare SHA-1 it was given as the ETag in If-Match would
// otherwise get 412 for a file that has not changed. The copy leaves the
// caller's request as it was sent.
func WithoutUnevaluatedPreconditions(r *http.Request) *http.Request {
	return WithoutHeaders(r, UnevaluatedPreconditions...)
}

// WithoutHeaders is r without the headers names, or r itself when it
// carries none of them. The copy leaves the caller's request as it was
// sent.
func WithoutHeaders(r *http.Request, names ...string) *http.Request {
	present := false
	for _, k := range names {
		present = present || len(r.Header.Values(k)) > 0
	}
	if !present {
		return r
	}
	out := r.WithContext(r.Context())
	out.Header = r.Header.Clone()
	for _, k := range names {
		out.Header.Del(k)
	}
	return out
}

// md5OptIn are the values of a request's first
// X-Artifactory-If-None-Match-MD5 line that Commons Lang's
// BooleanUtils.toBoolean reads as true, in any case, which is how 7.x reads
// the header (ArtifactoryRequestBase).
var md5OptIn = []string{"true", "on", "yes", "y", "t"}

// FileNotModified reports whether r's If-None-Match or If-Modified-Since
// finds a file unchanged, as 7.x's DownloadServiceImpl.isNotModified and
// isNonMatch decide it for every file download (7.161.26). An If-None-Match
// decides alone: it matches when one of its header lines, less a W/ prefix
// and every quote, is one of tags, the file's SHA-1 and any other tag the
// response sends for it, so neither "*" nor a list of tags on one line
// matches (ArtifactoryRequestBase.isNoneMatch and parseETagValue). A
// request whose first X-Artifactory-If-None-Match-MD5 line is true may name
// md5 instead (isSha1AndMd5NotMatch). Otherwise an If-Modified-Since
// matches when the file has not changed since, to the second (isNewerThan).
// An empty tag or md5 matches nothing.
func FileNotModified(r *http.Request, tags []string, md5 string, modified time.Time) bool {
	if lines := r.Header.Values("If-None-Match"); len(lines) > 0 {
		if flags := r.Header.Values("X-Artifactory-If-None-Match-MD5"); len(flags) == 0 ||
			!slices.Contains(md5OptIn, strings.ToLower(flags[0])) {
			md5 = ""
		}
		for _, line := range lines {
			if tag := ETagValue(line); tag != "" && (slices.Contains(tags, tag) || tag == md5) {
				return true
			}
		}
		return false
	}
	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	return err == nil && !modified.IsZero() && !modified.Truncate(time.Second).After(since)
}

// ETagValue is tag as 7.x's ArtifactoryRequestBase.parseETagValue reads
// it: trimmed, every W/ taken out when it starts with one, and every quote
// taken out (7.161.26).
func ETagValue(tag string) string {
	tag = strings.TrimSpace(tag)
	if strings.HasPrefix(tag, "W/") {
		tag = strings.ReplaceAll(tag, "W/", "")
	}
	return strings.ReplaceAll(tag, `"`, "")
}

// WriteNotModified answers 304 with the headers already set, less those a
// 304 does not carry, as http.ServeContent's 304 drops them: the body's
// type, length and encoding, and Last-Modified beside an ETag (RFC 9110
// section 15.4.5). A file download's 304 looks as it did when ServeContent
// decided it; only the decision is 7.x's (FileNotModified).
func WriteNotModified(w http.ResponseWriter) {
	h := w.Header()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding"} {
		delete(h, k)
	}
	if h.Get("ETag") != "" {
		delete(h, "Last-Modified")
	}
	w.WriteHeader(http.StatusNotModified)
}

// RangeRefusalWriter answers a file download's 416 as 7.x's
// RequestResponseHelper.sendBodyResponse does: Content-Range and the
// status alone, set and sent before any of the file's headers or body
// (7.161.26). http.ServeContent's 416 carries http.Error's text and the
// headers already set, so on a 416 it drops every other header that
// describes the file or a body and writes nothing.
type RangeRefusalWriter struct {
	http.ResponseWriter

	refused bool
}

// fileHeaders are the headers a file download sets before the range is
// known, which 7.x's 416 does not carry.
var fileHeaders = []string{
	"Content-Type", "Content-Disposition", "Content-Encoding", "ETag", "Last-Modified",
	"Accept-Ranges", "X-Content-Type-Options", "X-Artifactory-Filename",
	"X-Checksum-Sha1", "X-Checksum-Sha256", "X-Checksum-Md5",
}

// WriteHeader drops the file's headers from a 416 before it sends it.
func (w *RangeRefusalWriter) WriteHeader(status int) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.refused = true
		h := w.Header()
		for _, k := range fileHeaders {
			delete(h, k)
		}
		h.Set("Content-Length", "0")
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write drops a 416's body.
func (w *RangeRefusalWriter) Write(p []byte) (int, error) {
	if w.refused {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p) //nolint:wrapcheck // the writer's own error
}

// ReadFrom keeps the underlying writer's zero-copy path for a file's body,
// and drops a 416's.
func (w *RangeRefusalWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.refused {
		return io.Copy(io.Discard, r) //nolint:wrapcheck // the reader's own error
	}
	return io.Copy(w.ResponseWriter, r) //nolint:wrapcheck // a pass-through writer
}

// Unwrap is the writer it wraps, for http.ResponseController.
func (w *RangeRefusalWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
