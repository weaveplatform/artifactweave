package huggingfacehttp

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
)

// resolveETag is the ETag a resolve of s answers with: the LFS file's
// SHA-256, bare, as 7.117.12 answered a remote resolve with the SHA-256
// the Hub sends as X-Linked-Etag (RTFACT-31280), and otherwise the Git blob
// id, bare, the Hub's ETag of a file kept in Git [I]. huggingface_hub reads
// either form and keys its cache by the value (file_download.py,
// _normalize_etag), so the two match what the Hub would give it. decodeInfo
// and localInfo give every sibling one of them.
func resolveETag(s sibling) string {
	if s.LFS != nil {
		return s.LFS.SHA256
	}
	return s.BlobID
}

// resolveConditions decides r's If-None-Match against etag, the resolve's
// ETag, by the rule of 7.x's download (pkgroute.FileNotModified): a header
// line that, less a W/ and its quotes, is etag finds the file unchanged.
// It reports true for that answer, which the caller gives as a 304.
// Otherwise it returns the headers for the file's download to answer:
// without an If-None-Match that did not match, nor the If-Modified-Since
// it overrides, since the download would judge them against its own
// validators; with an If-Range naming etag taken off, so the range
// applies; and without Range when If-Range names another tag, as RFC 9110
// section 13.1.5 has a server ignore Range then. An If-Range date and an
// If-Modified-Since alone stay for the download to judge against the
// file's time, as they did before the resolve took the Hub's ETag [I]. r's
// own headers are left as they were sent.
func resolveConditions(r *http.Request, etag string) (http.Header, bool) {
	if len(r.Header.Values("If-None-Match")) > 0 {
		if pkgroute.FileNotModified(r, []string{etag}, "", time.Time{}) {
			return r.Header, true
		}
		r = pkgroute.WithoutHeaders(r, "If-None-Match", "If-Modified-Since")
	}
	ifRange := r.Header.Get("If-Range")
	switch {
	case ifRange == "":
	case pkgroute.ETagValue(ifRange) == etag:
		r = pkgroute.WithoutHeaders(r, "If-Range")
	default:
		if _, err := http.ParseTime(ifRange); err != nil {
			r = pkgroute.WithoutRange(r)
		}
	}
	return r.Header, false
}

// withHeader is r, the resolve's request or the chosen member's, with
// header in place of its own, as resolveConditions left them.
func withHeader(r *http.Request, header http.Header) *http.Request {
	out := new(http.Request)
	*out = *r
	out.Header = header
	return out
}

// revalidate runs, before a file of s's revision is answered, the checks a
// remote member's info read left to run at delivery: the fence on the
// credential it was read with, then the read check of path. A local
// member's selection has neither.
func (s selected) revalidate(ctx context.Context, path string) error {
	if s.fence != nil {
		if err := s.fence(ctx); err != nil {
			return err
		}
	}
	if s.check != nil {
		return s.check(ctx, path)
	}
	return nil
}

// downloadHeaders are the headers the item routes' download sets on a
// served file that a resolve does not send: 7.117.12 answered a remote
// resolve with Content-Type, Content-Length, its own ETag and X-Repo-Commit
// alone (RTFACT-31280), with no X-Artifactory-Filename, Content-Disposition,
// X-Checksum-* or Last-Modified (#1581).
var downloadHeaders = []string{
	"X-Artifactory-Filename", "Content-Disposition",
	"X-Checksum-Sha1", "X-Checksum-Sha256", "X-Checksum-Md5", "Last-Modified",
}

// resolveWriter gives a resolve answered by the item routes' download the
// resolve's headers: as a 200, 206 or 304 goes out, it drops
// downloadHeaders and sends etag as the ETag, in place of the download's
// SHA-1. Accept-Ranges stays: the resolve serves ranges, which
// RTFACT-31280 asks 7.x to declare, its capture being of the fault that
// ignored them [I]. Any other status, a redirect to storage or a refusal,
// goes out as the download set it.
type resolveWriter struct {
	http.ResponseWriter

	etag string
	sent bool
}

func (w *resolveWriter) headers(status int) {
	if w.sent {
		return
	}
	w.sent = true
	switch status {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		h := w.Header()
		for _, k := range downloadHeaders {
			delete(h, k)
		}
		h.Set("ETag", w.etag)
	}
}

// WriteHeader sends the resolve's headers with the status.
func (w *resolveWriter) WriteHeader(status int) {
	w.headers(status)
	w.ResponseWriter.WriteHeader(status)
}

// Write sends the resolve's headers when a 200 goes out with these bytes.
func (w *resolveWriter) Write(p []byte) (int, error) {
	w.headers(http.StatusOK)
	return w.ResponseWriter.Write(p) //nolint:wrapcheck // a pass-through writer
}

// ReadFrom keeps the underlying writer's zero-copy path for the file's
// body.
func (w *resolveWriter) ReadFrom(r io.Reader) (int64, error) {
	w.headers(http.StatusOK)
	return io.Copy(w.ResponseWriter, r) //nolint:wrapcheck // a pass-through writer
}

// Unwrap is the writer it wraps, for http.ResponseController.
func (w *resolveWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
