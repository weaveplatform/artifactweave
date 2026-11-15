package vcshttp

import (
	"io"
	"net/http"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// vcsArchiveFile is a VCS archive as a download answers it: its bytes and
// length, its extension, the name 7.x gives it (archiveName) and the
// checksums its read knows.
type vcsArchiveFile struct {
	body      io.ReadCloser
	size      int64
	name, ext string
	sums      kernel.Checksums
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

// resultChecksums are the checksums result knows for an archive: a cached
// archive's recorded ones, or those a stream computed or declared.
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

// vcsArchiveHeaders is every archive download's 200 headers, in the
// generated ResponseHeaders' field order, so that it converts to each.
type vcsArchiveHeaders struct {
	ContentDisposition   *string
	XArtifactoryFilename *string
	XChecksumMd5         *string
	XChecksumSha1        *string
	XChecksumSha256      *string
}

// headers are the file headers 7.x sends on an archive download, in the
// package download service's form: X-Artifactory-Filename, a
// Content-Disposition with the quoted name alone
// (itemkit.PackageDisposition), as bazelbuild/bazel#22421 shows for
// downloadTag, and each checksum the read knows. A 7.x downloadTag
// answered X-Checksum-Sha256 (conan-io/conan#11656); the SHA-1 and MD5
// come with it, as RequestResponseHelper.setBasicHeaders sets all three
// (7.161.26) [I]. A checksum the read does not know is left out rather than
// sent empty.
func (a vcsArchiveFile) headers() vcsArchiveHeaders {
	h := vcsArchiveHeaders{
		ContentDisposition:   new(itemkit.PackageDisposition(a.name)),
		XArtifactoryFilename: new(a.name),
	}
	for _, c := range []struct {
		value string
		to    **string
	}{
		{a.sums.MD5, &h.XChecksumMd5},
		{a.sums.SHA1, &h.XChecksumSha1},
		{a.sums.SHA256, &h.XChecksumSha256},
	} {
		if c.value != "" {
			*c.to = new(c.value)
		}
	}
	return h
}

// zip reports whether the archive is a zip, which answers application/zip;
// a tar.gz answers application/x-gzip, the types 7.x's MIME table gives
// the two (bazelbuild/bazel#22421 shows the tar.gz).
func (a vcsArchiveFile) zip() bool { return a.ext == "zip" }

// write answers the archive on w, as VCSNested serves the subgroup forms
// the generated routes cannot match, with the generated routes' headers.
func (a vcsArchiveFile) write(w http.ResponseWriter) error {
	defer func() { _ = a.body.Close() }()
	h := w.Header()
	h.Set("Content-Type", "application/x-gzip")
	if a.zip() {
		h.Set("Content-Type", "application/zip")
	}
	if a.size > 0 {
		h.Set("Content-Length", strconv.FormatInt(a.size, 10))
	}
	headers := a.headers()
	for k, v := range map[string]*string{
		"Content-Disposition":    headers.ContentDisposition,
		"X-Artifactory-Filename": headers.XArtifactoryFilename,
		"X-Checksum-Md5":         headers.XChecksumMd5, "X-Checksum-Sha1": headers.XChecksumSha1,
		"X-Checksum-Sha256": headers.XChecksumSha256,
	} {
		if v != nil {
			h.Set(k, *v)
		}
	}
	_, err := io.Copy(w, a.body)
	return err //nolint:wrapcheck // the client's own write error, noted by the caller
}
