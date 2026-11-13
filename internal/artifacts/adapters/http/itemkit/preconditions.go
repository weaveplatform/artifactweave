package itemkit

import "net/http"

// unevaluatedPreconditions are the conditional headers Artifactory's
// download never reads. 6.20 decides a download's conditional answer in
// DownloadServiceImpl.isNotModified, from If-Modified-Since and
// If-None-Match alone, for a local, cached or remote file, GET or HEAD, and
// never answers 412. JFrog closed RTFACT-17259, which asked for If-Match
// and If-Unmodified-Since, as "No plans to implement in near future".
// 7.125.6 put If-None-Match before If-Modified-Since, as RFC 9110 orders
// them (RTDEV-64461), and named no other precondition, and no 7.x document
// or KB article says a download evaluates either [I].
var unevaluatedPreconditions = []string{"If-Match", "If-Unmodified-Since"}

// withoutUnevaluatedPreconditions is r without unevaluatedPreconditions, so
// that a file download answers neither with 412, whatever tag or date they
// carry. http.ServeContent and answerPreconditions apply every RFC 9110
// precondition, so without this a client sending back the bare SHA-1 it
// was given as the ETag in If-Match would get 412 for a file that has not
// changed. If-None-Match, If-Modified-Since and If-Range stay, and answer
// as before. The copy leaves the caller's request as it was sent.
func withoutUnevaluatedPreconditions(r *http.Request) *http.Request {
	present := false
	for _, k := range unevaluatedPreconditions {
		present = present || len(r.Header.Values(k)) > 0
	}
	if !present {
		return r
	}
	out := r.WithContext(r.Context())
	out.Header = r.Header.Clone()
	for _, k := range unevaluatedPreconditions {
		out.Header.Del(k)
	}
	return out
}
