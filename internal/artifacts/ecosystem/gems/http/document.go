package gemshttp

import (
	"bytes"
	"crypto/md5"  //nolint:gosec // X-Checksum-Md5 and the compact index's ETag name the bytes, as 7.x sends them
	"crypto/sha1" //nolint:gosec // X-Checksum-Sha1 names the bytes, as 7.x sends it
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"path"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// storedDocument reports the metadata documents 7.x keeps as files in the
// repository: the spec index files and quick gemspecs, which reindex
// re-creates (JFrog's RubyGems docs), and the compact index, whose versions
// file a reindex writes and later deploys append to (RTFACT-29884) and
// which 7.41.13 answered with a stored file's headers (RTFACT-27459). The
// JSON API is computed per request, as api/v1/dependencies is, with none of
// them (RTFACT-27459).
func storedDocument(p string) bool { return specIndex(p) || compactIndex(p) }

// documentType is the Content-Type of the stored document at p. 7.41.13
// typed versions application/octet-stream (RTFACT-27459), the type 7.x's
// MIME table gives a name with no row, as it gives names and info/<gem>
// [I]. The spec index files and quick gemspecs take their row, as 7.x
// types every stored file by its name (DbStoringRepoMixin.setMimeType,
// 7.161.26): .gz is application/x-gzip and .rz, a row the table carries
// for these gemspecs, application/x-ruby-marshal [I].
func documentType(p string) string {
	if compactIndex(p) {
		return "application/octet-stream"
	}
	return item.MimeType(p)
}

// serveDocument answers the stored document at p, whose bytes are b and
// whose type is media (documentType), as 7.x answers one: the stored
// file's headers, in the package download service's form, that 7.41.13
// sent on GET /api/gems/<repo>/versions
// (RTFACT-27459): X-Artifactory-Filename, a Content-Disposition with the
// quoted name alone (itemkit.PackageDisposition), the three checksums of
// the bytes, Last-Modified, Accept-Ranges and Cache-Control: no-store. The
// compact index's ETag is the quoted MD5 there, equal to X-Checksum-Md5,
// which Bundler compares with its own copy's; a spec index file's is the
// bare SHA-1 7.x's download gives any stored file
// (RequestResponseHelper.setBasicHeaders, 7.161.26) [I].
//
// The document is built per request, from no stored file whose time
// changes with it (a deleted gem leaves none behind), so Last-Modified is
// the time it was built and If-Modified-Since never answers 304: an older
// time could withhold a change from RubyGems, which asks for specs.4.8.gz
// with it. 7.41.6 answered that request 304 from its stored file
// (RTFACT-29176) [I]. Repr-Digest stays: Bundler 2.5 and later verify a
// ranged append of versions with it, and the 7.41.13 capture predates that
// release [I].
//
// The conditional answer is otherwise the download's
// (pkgroute.FileNotModified): If-None-Match against the ETag or the SHA-1.
// If-Match and If-Unmodified-Since are never read, a HEAD ignores Range,
// and a range past the end answers 416 with Content-Range alone.
func serveDocument(w http.ResponseWriter, r *http.Request, p, media string, b []byte) {
	sums := checksums(b)
	name := path.Base(p)
	modified := time.Now()
	h := w.Header()
	h.Set("Content-Type", media)
	h.Set("Cache-Control", "no-store")
	pkgroute.SetChecksums(h, sums)
	h.Set("X-Artifactory-Filename", name)
	h.Set("Content-Disposition", itemkit.PackageDisposition(name))
	digest, _ := hex.DecodeString(sums.SHA256)
	h.Set("Repr-Digest", `sha-256="`+base64.StdEncoding.EncodeToString(digest)+`"`)
	tags := []string{sums.SHA1}
	if compactIndex(p) {
		h.Set("ETag", `"`+sums.MD5+`"`)
		tags = append(tags, sums.MD5)
	} else {
		w, r = itemkit.BareETag(w, r, sums.SHA1)
	}
	if pkgroute.FileNotModified(r, tags, sums.MD5, time.Time{}) {
		pkgroute.WriteNotModified(w)
		return
	}
	// Decided above, by 7.x's rule, so ServeContent must not decide again.
	r = pkgroute.WithoutHeaders(r, "If-None-Match", "If-Modified-Since")
	itemkit.ServeContent(&rangeRefusal{RangeRefusalWriter: pkgroute.RangeRefusalWriter{
		ResponseWriter: w,
	}}, r, name, modified, bytes.NewReader(b))
}

// checksums are b's three digests, as 7.x records a stored file's.
func checksums(b []byte) kernel.Checksums {
	s1 := sha1.Sum(b) //nolint:gosec // a checksum header, not a security control
	s256 := sha256.Sum256(b)
	m5 := md5.Sum(b) //nolint:gosec // a checksum header, not a security control
	return kernel.Checksums{
		SHA1: hex.EncodeToString(s1[:]), SHA256: hex.EncodeToString(s256[:]),
		MD5: hex.EncodeToString(m5[:]),
	}
}

// rangeRefusal also drops Cache-Control and Repr-Digest from a 416: 7.x
// answers a range past the end before it sets any of the file's headers
// (RequestResponseHelper.sendBodyResponse, 7.161.26).
type rangeRefusal struct {
	pkgroute.RangeRefusalWriter
}

// WriteHeader drops the two headers from a 416 before it sends it.
func (w *rangeRefusal) WriteHeader(status int) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Del("Cache-Control")
		w.Header().Del("Repr-Digest")
	}
	w.RangeRefusalWriter.WriteHeader(status)
}
