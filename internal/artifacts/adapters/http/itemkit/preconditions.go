package itemkit

import (
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// withoutUnevaluatedPreconditions is r without the conditional headers
// Artifactory's download never reads, If-Match and If-Unmodified-Since
// (pkgroute.UnevaluatedPreconditions), so that a file download answers
// neither with 412, whatever tag or date they carry. If-None-Match,
// If-Modified-Since and If-Range stay.
func withoutUnevaluatedPreconditions(r *http.Request) *http.Request {
	return pkgroute.WithoutUnevaluatedPreconditions(r)
}

// downloadRequest is r as Artifactory's download reads it: without
// pkgroute.UnevaluatedPreconditions and, on a HEAD, without Range and If-Range. A
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

// serveChecksum answers a GET or HEAD of a checksum file at name whose
// content is value, the checksum of a file whose checksums are sums (those
// known) and which last changed at modified, as 7.x answers one on a local,
// virtual and remote repository alike (Artifactory OSS 7.161.26 sources,
// DownloadServiceImpl.respond and respondForChecksumRequest, and
// RequestResponseHelper):
//   - its file's isNotModified decides first (fileNotModified), and its 304
//     is the file's: sendNotModifiedResponse is handed the file's resource,
//     so it carries the file's headers (fileHeaders);
//   - If-Match and If-Unmodified-Since are never read;
//   - a HEAD answers 200 with the checksum's type and length and otherwise
//     the file's headers: sendHeadResponse is handed a ChecksumResource,
//     whose information is its file's (ResolvedResource without
//     overrideResourceInfoChecksums), and it never reads Range;
//   - a GET is sendBodyResponseForChecksum's: the checksum's type and
//     length, Last-Modified the time of the response, Range applied with
//     any If-Range ignored ("If-Range is not supported"), none of the
//     file's headers, Accept-Ranges included, and a 416 without a body.
func serveChecksum(
	w http.ResponseWriter,
	r *http.Request,
	name, value string,
	sums kernel.Checksums,
	modified time.Time,
) {
	r = withoutUnevaluatedPreconditions(r)
	artifact, _, _ := item.Sidecar(name)
	h := w.Header()
	if fileNotModified(r, sums, modified) {
		fileHeaders(h, path.Base(artifact), sums, modified)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", checksumType(r, name))
	if r.Method == http.MethodHead {
		fileHeaders(h, path.Base(artifact), sums, modified)
		h.Set("Content-Length", strconv.Itoa(len(value)))
		w.WriteHeader(http.StatusOK)
		return
	}
	h.Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	// Decided above against the file, so ServeContent must not decide them
	// again against the checksum, which has no ETag of its own.
	r = withoutHeaders(r, "If-None-Match", "If-Modified-Since", "If-Range")
	http.ServeContent(&checksumBodyWriter{
		ResponseWriter: w, contentType: h.Get("Content-Type"),
		date: h.Get("Last-Modified"),
	}, r, "", time.Time{}, strings.NewReader(value))
}

// fileHeaders sets the headers 7.x's download sends from a file's own
// information (RequestResponseHelper.setBasicHeaders, 7.161.26): its
// Last-Modified, its SHA-1 as the ETag, bare as 7.x sends it, its
// checksums, its name, Content-Disposition and Accept-Ranges, as this
// server's file download sends them (serveFile). A checksum or a time that
// is not known is left out rather than sent empty.
func fileHeaders(h http.Header, name string, sums kernel.Checksums, modified time.Time) {
	if !modified.IsZero() {
		h.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	}
	if sums.SHA1 != "" {
		h.Set("ETag", sums.SHA1)
	}
	for k, v := range map[string]string{
		"X-Checksum-Sha1": sums.SHA1, "X-Checksum-Sha256": sums.SHA256, "X-Checksum-Md5": sums.MD5,
	} {
		if v != "" {
			h.Set(k, v)
		}
	}
	h.Set("X-Artifactory-Filename", name)
	h.Set("Content-Disposition", blob.Disposition(name))
	h.Set("Accept-Ranges", "bytes")
}

// checksumBodyWriter answers a checksum GET as 7.x's
// sendBodyResponseForChecksum does where http.ServeContent differs
// (7.161.26): it sends no Accept-Ranges, which only setBasicHeaders sends,
// and it answers a range that does not overlap with 416, the checksum's
// type and date and Content-Range only, where ServeContent sends
// http.Error's text in its place.
type checksumBodyWriter struct {
	http.ResponseWriter

	contentType, date string
	unsatisfiable     bool
}

func (w *checksumBodyWriter) WriteHeader(status int) {
	h := w.Header()
	h.Del("Accept-Ranges")
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.unsatisfiable = true
		h.Del("X-Content-Type-Options")
		h.Set("Content-Type", w.contentType)
		h.Set("Last-Modified", w.date)
		h.Set("Content-Length", "0")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *checksumBodyWriter) Write(p []byte) (int, error) {
	if w.unsatisfiable {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p) //nolint:wrapcheck // the writer's own error
}

// fileNotModified reports whether r's If-None-Match or If-Modified-Since
// finds the file whose checksums are sums, and which last changed at
// modified, unchanged, as 7.x's DownloadServiceImpl.isNotModified and
// isNonMatch decide it (7.161.26). An If-None-Match decides alone: it
// matches when one of its header lines, less a W/ prefix and its quotes,
// is the file's SHA-1, so neither "*" nor a list of tags on one line
// matches (ArtifactoryRequestBase.isNoneMatch and parseETagValue). A
// request whose first X-Artifactory-If-None-Match-MD5 line is true, as
// Commons Lang's BooleanUtils.toBoolean reads it, may name the MD5
// instead (isSha1AndMd5NotMatch). Otherwise an If-Modified-Since matches
// when the file has not changed since, to the second (isNewerThan).
func fileNotModified(r *http.Request, sums kernel.Checksums, modified time.Time) bool {
	if tags := r.Header.Values("If-None-Match"); len(tags) > 0 {
		md5 := ""
		if flags := r.Header.Values("X-Artifactory-If-None-Match-MD5"); len(flags) > 0 &&
			slices.Contains([]string{"true", "on", "yes", "y", "t"}, strings.ToLower(flags[0])) {
			md5 = sums.MD5
		}
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if strings.HasPrefix(tag, "W/") {
				tag = strings.ReplaceAll(tag, "W/", "")
			}
			tag = strings.ReplaceAll(tag, `"`, "")
			if (sums.SHA1 != "" && tag == sums.SHA1) || (md5 != "" && tag == md5) {
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
