package itemkit

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
)

// unevaluatedPreconditions are the conditional headers Artifactory's
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
var unevaluatedPreconditions = []string{"If-Match", "If-Unmodified-Since"}

// withoutUnevaluatedPreconditions is r without unevaluatedPreconditions, so
// that a file download answers neither with 412, whatever tag or date they
// carry. http.ServeContent and answerPreconditions apply every RFC 9110
// precondition, so without this a client sending back the bare SHA-1 it
// was given as the ETag in If-Match would get 412 for a file that has not
// changed. If-None-Match, If-Modified-Since and If-Range stay, and answer
// as before. The copy leaves the caller's request as it was sent.
func withoutUnevaluatedPreconditions(r *http.Request) *http.Request {
	return withoutHeaders(r, unevaluatedPreconditions...)
}

// downloadRequest is r as Artifactory's download reads it: without
// unevaluatedPreconditions and, on a HEAD, without Range and If-Range. A
// HEAD answers 200 with the whole length, never 206 or 416: 7.x answers a
// HEAD through RequestResponseHelper.sendHeadResponse, which never reads
// Range (7.161.26), and RFC 9110 section 14.2 has a server ignore Range on
// a method range handling is not defined for, which is every method but
// GET, while http.ServeContent applies it whatever the method.
func downloadRequest(r *http.Request) *http.Request {
	r = withoutUnevaluatedPreconditions(r)
	if r.Method == http.MethodHead {
		r = pkgroute.WithoutRange(r)
	}
	return r
}

// ServeContent is http.ServeContent for content a package type's route
// builds itself, such as an index or a metadata document, answered as
// Artifactory's download answers the file it serves (downloadRequest):
// If-Match and If-Unmodified-Since never answer 412, and a HEAD ignores
// Range. If-None-Match, If-Modified-Since, If-Range and a GET's Range
// answer as http.ServeContent answers them, against the ETag already in
// w's headers, if any, and modified.
//
// 7.x calculates most of these indexes into the repository and serves them
// through its download, as any file. The routes it computes per request
// (Cargo's index, Composer's and Bazel's metadata, the RubyGems compact
// index and the others) are not in its OSS sources, and no document,
// release note or KB article says that any of them reads If-Match or
// If-Unmodified-Since, or answers a HEAD with a range [I].
func ServeContent(
	w http.ResponseWriter,
	r *http.Request,
	name string,
	modified time.Time,
	content io.ReadSeeker,
) {
	http.ServeContent(w, downloadRequest(r), name, modified, content)
}

// serveChecksum answers a GET or HEAD of a checksum file whose content is
// value, the checksum of a file whose SHA-1 is sha1 and which last changed
// at modified, as 7.x answers one on a local, virtual and remote
// repository alike (DownloadServiceImpl.respond, 7.161.26):
//   - its file's isNotModified decides first, so an If-None-Match naming
//     the file's SHA-1, or an If-Modified-Since the file has not changed
//     since, answers 304 (fileNotModified);
//   - If-Match and If-Unmodified-Since are never read;
//   - a HEAD answers 200 with the checksum's length (sendHeadResponse,
//     which never reads Range);
//   - a GET applies Range (sendBodyResponseForChecksum).
//
// The 304 carries the Last-Modified the 200 does. An empty sha1 matches no
// If-None-Match, as isNoneMatch treats a blank SHA-1 (7.161.26).
func serveChecksum(
	w http.ResponseWriter,
	r *http.Request,
	name, value, sha1 string,
	modified time.Time,
) {
	r = downloadRequest(r)
	if fileNotModified(r, sha1, modified) {
		if !modified.IsZero() {
			w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
		}
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", checksumType(r, name))
	// Decided above against the file, so ServeContent must not decide
	// them again against the checksum, which has no ETag of its own.
	r = withoutHeaders(r, "If-None-Match", "If-Modified-Since")
	http.ServeContent(w, r, "", modified, strings.NewReader(value))
}

// fileNotModified reports whether r's If-None-Match or If-Modified-Since
// finds the file unchanged, as 7.x's DownloadServiceImpl.isNotModified and
// isNonMatch decide it (7.161.26). An If-None-Match decides alone: it
// matches when one of its header lines, less a W/ prefix and its quotes,
// is the file's SHA-1, so neither "*" nor a list of tags on one line
// matches (ArtifactoryRequestBase.isNoneMatch and parseETagValue).
// Otherwise an If-Modified-Since matches when the file has not changed
// since, to the second (isNewerThan).
func fileNotModified(r *http.Request, sha1 string, modified time.Time) bool {
	if tags := r.Header.Values("If-None-Match"); len(tags) > 0 {
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if strings.HasPrefix(tag, "W/") {
				tag = strings.ReplaceAll(tag, "W/", "")
			}
			if sha1 != "" && strings.ReplaceAll(tag, `"`, "") == sha1 {
				return true
			}
		}
		return false
	}
	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	return err == nil && !modified.IsZero() && !modified.Truncate(time.Second).After(since)
}

// withoutHeaders is r without the headers names, or r itself when it
// carries none of them. The copy leaves the caller's request as it was
// sent.
func withoutHeaders(r *http.Request, names ...string) *http.Request {
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
