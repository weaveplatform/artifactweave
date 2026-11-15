package vcshttp

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // X-Checksum-Md5 names the bytes, as 7.x sends it
	"crypto/sha1" //nolint:gosec // X-Checksum-Sha1 and the ETag name the bytes, as 7.x sends them
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
)

// vcsServed is a file a VCS download answers: the archive of a tag, a
// branch, a release or a commit, or one file within a tag or branch. Its
// body seeks when the remote source or the blob store gives one that does,
// which every validated or cached file is.
type vcsServed struct {
	body        io.ReadCloser
	size        int64
	name, media string
	sums        kernel.Checksums
	modified    time.Time
	// used records the cached file's use once a GET has delivered all of it
	// (source.Result.Used).
	used func(context.Context) error
}

// archiveName is the name 7.x gives the archive of ref, a tag, a branch or
// a commit of the project name, in ext: GitRef.filename (7.161.26, as in
// 6.20) names a tag's <name>-<tag>.<ext> and a branch's
// <name>-<branch>-<commit>.<ext>, and a tag archive downloaded through
// downloadTag answered X-Artifactory-Filename: rules_java-7.5.0.tar.gz
// (bazelbuild/bazel#22421). A commit, which GitRef names as a tag, is
// <name>-<commit>.<ext>, and a release, the tag of its name, as that tag
// [I]. 7.177.2, a cloud release, caches a tag at <name>-<tag>-<commit>;
// the self-managed releases this follows do not [I].
func archiveName(name, kind, ref, commit, ext string) string {
	if kind == "heads" {
		return name + "-" + ref + "-" + commit + "." + ext
	}
	return name + "-" + ref + "." + ext
}

// archiveMedia is the type 7.x's MIME table gives an archive of ext:
// application/zip for a zip and application/x-gzip for a tar.gz
// (bazelbuild/bazel#22421 shows the tar.gz).
func archiveMedia(ext string) string {
	if ext == "zip" {
		return "application/zip"
	}
	return "application/x-gzip"
}

// resultChecksums are the checksums result knows for a file: a cached
// file's recorded ones, or those a stream computed or declared.
func resultChecksums(result source.Result) kernel.Checksums {
	switch {
	case result.Item != nil:
		return result.Item.Actual
	case result.Stream != nil:
		return result.Stream.Checksums
	default:
		return kernel.Checksums{}
	}
}

// resultModified is when result's file last changed: a cached file's
// time, or the time a stream declares, zero when it declares none.
func resultModified(result source.Result) time.Time {
	switch {
	case result.Item != nil:
		return result.Item.Modified
	case result.Stream != nil:
		return result.Stream.Modified
	default:
		return time.Time{}
	}
}

// checksums are data's three digests, as 7.x records a stored file's.
func checksums(data []byte) kernel.Checksums {
	s1 := sha1.Sum(data) //nolint:gosec // a checksum header, not a security control
	s256 := sha256.Sum256(data)
	m5 := md5.Sum(data) //nolint:gosec // a checksum header, not a security control
	return kernel.Checksums{
		SHA1: hex.EncodeToString(s1[:]), SHA256: hex.EncodeToString(s256[:]),
		MD5: hex.EncodeToString(m5[:]),
	}
}

// inMemory is a vcsServed of data, which a selected file always is.
func inMemory(data []byte) io.ReadCloser {
	return struct {
		io.ReadSeeker
		io.Closer
	}{bytes.NewReader(data), io.NopCloser(nil)}
}

// serve answers f on w for r as 7.x's download answers a file
// (RequestResponseHelper.setBasicHeaders and DownloadServiceImpl,
// 7.161.26), in the package download service's form the VCS downloads take:
// the type, X-Artifactory-Filename, a Content-Disposition with the quoted
// name alone (itemkit.PackageDisposition), as bazelbuild/bazel#22421 shows
// for downloadTag, each checksum that is known, X-Checksum-Sha256 among
// them (conan-io/conan#11656), the SHA-1 as a bare ETag, Last-Modified and
// Accept-Ranges. The captures show only the name, the type and the
// SHA-256; the rest is what setBasicHeaders sets beside them [I].
//
// The conditional answer is the download's (pkgroute.FileNotModified):
// If-None-Match against the SHA-1, else If-Modified-Since against the
// file's time, with a 304. If-Match and If-Unmodified-Since are never
// read, a HEAD ignores Range, a range answers 206, and a range past the end
// 416 with Content-Range alone (pkgroute.RangeRefusalWriter). A body that
// cannot seek is answered whole, without Accept-Ranges.
func (f vcsServed) serve(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = f.body.Close() }()
	ctx := r.Context()
	r = pkgroute.WithoutUnevaluatedPreconditions(r)
	if r.Method == http.MethodHead {
		r = pkgroute.WithoutRange(r)
	}
	h := w.Header()
	h.Set("Content-Type", f.media)
	pkgroute.SetChecksums(h, f.sums)
	h.Set("X-Artifactory-Filename", f.name)
	h.Set("Content-Disposition", itemkit.PackageDisposition(f.name))
	if !f.modified.IsZero() {
		h.Set("Last-Modified", f.modified.UTC().Format(http.TimeFormat))
	}
	if f.sums.SHA1 != "" {
		w, r = itemkit.BareETag(w, r, f.sums.SHA1)
	}
	if pkgroute.FileNotModified(r, []string{f.sums.SHA1}, f.sums.MD5, f.modified) {
		pkgroute.WriteNotModified(w)
		return nil
	}
	// Decided above, by 7.x's rule, so ServeContent must not decide again.
	r = pkgroute.WithoutHeaders(r, "If-None-Match", "If-Modified-Since")
	d := &vcsDelivery{ResponseWriter: &pkgroute.RangeRefusalWriter{ResponseWriter: w}}
	if content, ok := f.body.(io.ReadSeeker); ok {
		itemkit.ServeContent(d, r, f.name, f.modified, content)
	} else {
		if f.size >= 0 {
			h.Set("Content-Length", strconv.FormatInt(f.size, 10))
		}
		d.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(d, f.body)
		}
	}
	if d.complete(r) && f.used != nil {
		_ = f.used(ctx)
	}
	return d.err
}

// vcsDelivery records what a download wrote, so that only a GET that
// delivered the whole file or range counts as the cached file's use.
type vcsDelivery struct {
	http.ResponseWriter

	status int
	bytes  int64
	err    error
}

// WriteHeader records the status.
func (d *vcsDelivery) WriteHeader(status int) {
	if d.status == 0 {
		d.status = status
	}
	d.ResponseWriter.WriteHeader(status)
}

// Write records the bytes and the client's write error.
func (d *vcsDelivery) Write(p []byte) (int, error) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	n, err := d.ResponseWriter.Write(p)
	d.bytes += int64(n)
	if err != nil && d.err == nil {
		d.err = err
	}
	return n, err //nolint:wrapcheck // a pass-through writer
}

// Unwrap is the writer it wraps, for http.ResponseController.
func (d *vcsDelivery) Unwrap() http.ResponseWriter { return d.ResponseWriter }

// complete reports whether a GET delivered a 200 or 206 in full.
func (d *vcsDelivery) complete(r *http.Request) bool {
	if r.Method != http.MethodGet || d.err != nil ||
		d.status != http.StatusOK && d.status != http.StatusPartialContent {
		return false
	}
	n, err := strconv.ParseInt(d.Header().Get("Content-Length"), 10, 64)
	return err != nil || n == d.bytes
}

// requestKey carries a VCS download's request to the operation that
// answers it (KeepRequest).
type requestKey struct{}

// fileOperations are the generated operations that answer a file: every
// archive download and every file within a tag or branch.
var fileOperations = map[string]bool{
	"DownloadVcsTag":                              true,
	"DownloadVcsTagAlternative":                   true,
	"DownloadVcsTagFromSubgroupRepository":        true,
	"DownloadVcsRelease":                          true,
	"DownloadVcsBranch":                           true,
	"DownloadVcsBranchAlternative":                true,
	"DownloadVcsBranchFromSubgroupRepository":     true,
	"DownloadVcsCommit":                           true,
	"DownloadFileWithinVcsTag":                    true,
	"DownloadFileWithinVcsTagAlternative":         true,
	"DownloadFileInVcsTagInSubgroupRepository":    true,
	"DownloadFileWithinVcsBranch":                 true,
	"DownloadFileWithinVcsBranchAlternative":      true,
	"DownloadFileInVcsBranchInSubgroupRepository": true,
}

// KeepRequest is the generated server's strict middleware that gives the
// VCS file downloads their request, whose If-None-Match, If-Modified-Since,
// Range and If-Range decide the answer (vcsServed.serve) and which a strict
// operation does not otherwise see. Every other operation passes as it
// was.
func KeepRequest(f artgen.StrictHandlerFunc, operationID string) artgen.StrictHandlerFunc {
	if !fileOperations[operationID] {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		return f(context.WithValue(ctx, requestKey{}, r), w, r, request)
	}
}

// requestFrom is the request KeepRequest kept in ctx, or a plain GET for
// a caller that calls the operation without one.
func requestFrom(ctx context.Context) *http.Request {
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		return r
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	return r
}

// vcsResponse answers a VCS file download's operation with its file, for
// the request it was asked by.
type vcsResponse struct {
	file vcsServed
	r    *http.Request
}

func (v vcsResponse) visit(w http.ResponseWriter) error { return v.file.serve(w, v.r) }

func (v vcsResponse) VisitDownloadVcsTagResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadVcsTagAlternativeResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadVcsTagFromSubgroupRepositoryResponse(
	w http.ResponseWriter,
) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadVcsReleaseResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadVcsBranchResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadVcsBranchAlternativeResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadVcsBranchFromSubgroupRepositoryResponse(
	w http.ResponseWriter,
) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadVcsCommitResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadFileWithinVcsTagResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadFileWithinVcsTagAlternativeResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadFileInVcsTagInSubgroupRepositoryResponse(
	w http.ResponseWriter,
) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadFileWithinVcsBranchResponse(w http.ResponseWriter) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadFileWithinVcsBranchAlternativeResponse(
	w http.ResponseWriter,
) error {
	return v.visit(w)
}

func (v vcsResponse) VisitDownloadFileInVcsBranchInSubgroupRepositoryResponse(
	w http.ResponseWriter,
) error {
	return v.visit(w)
}
