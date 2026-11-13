package pkgroute

import "net/http"

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
