package condahttp

import (
	"bytes"
	"crypto/md5"  //nolint:gosec // X-Checksum-Md5 names the bytes, as 7.x sends it
	"crypto/sha1" //nolint:gosec // X-Checksum-Sha1 and the ETag name the bytes, as 7.x sends them
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// document is a channel index document as this route answers it: 7.x
// calculates a local channel's documents into the repository, caches a
// remote's and keeps a virtual's merge, and serves each through its
// download as a stored file (JFrog's Conda repository docs; RTFACT-21415,
// RTFACT-21412, RTFACT-21417). Here the document is built per request from
// the records the caller may read, so its validators are those of the
// bytes served, and modified is when the stored or cached index it was
// built from last changed, zero when there is none.
type document struct {
	name, media string
	body        []byte
	modified    time.Time
}

// cacheControl is the Cache-Control 7.x sends on name. Its download sends
// no-store on an expirable resource alone
// (RequestResponseHelper.updateResponseFromRepoResource, 7.161.26), and
// CondaSpecification.isExpirable takes the repodata.json and
// current_repodata.json families, plain and compressed (RTFACT-31453's
// analysis of RepoDataFileKind); 7.33.12 sent it on repodata.json
// (mamba-org/mamba#2105). The CEP-16 documents came later than that
// analysis and no source shows theirs, so they keep the answers they had:
// the shard index is never stored by a shared cache, and a shard, named by
// its digest, never changes but must be revalidated [I].
func cacheControl(name string) string {
	switch {
	case name == condafmt.ShardIndexFilename:
		return "private, no-store"
	case condafmt.IsShardFilename(name):
		return "private, max-age=0, must-revalidate, immutable"
	default:
		return "no-store"
	}
}

// serveDocument answers d as 7.x's download answers a stored file
// (RequestResponseHelper.setBasicHeaders and DownloadServiceImpl, 7.161.26),
// in the package download service's form: X-Artifactory-Filename, a
// Content-Disposition with the quoted name alone
// (itemkit.PackageDisposition), the three checksums of the bytes, their
// bare SHA-1 as the ETag, Last-Modified when it is known and Accept-Ranges.
// 7.33.12 answered GET /api/conda/<repo>/noarch/repodata.json so
// (mamba-org/mamba#2105). The conditional answer is the download's
// (pkgroute.FileNotModified): If-None-Match against the SHA-1, else
// If-Modified-Since against modified, so a conda or mamba client that sends
// the ETag back gets a 304, as 7.x's virtual repository answers one
// (RTFACT-18716). If-Match and If-Unmodified-Since are never read, a HEAD
// ignores Range, and a range past the end answers 416 with Content-Range
// alone.
func serveDocument(w http.ResponseWriter, r *http.Request, d document) {
	sums := checksums(d.body)
	h := w.Header()
	h.Set("Content-Type", d.media)
	h.Set("Cache-Control", cacheControl(d.name))
	pkgroute.SetChecksums(h, sums)
	h.Set("X-Artifactory-Filename", d.name)
	h.Set("Content-Disposition", itemkit.PackageDisposition(d.name))
	w, r = itemkit.BareETag(w, r, sums.SHA1)
	if pkgroute.FileNotModified(r, []string{sums.SHA1}, sums.MD5, d.modified) {
		// As the item route's 304: the file's headers, less those
		// WriteNotModified drops.
		pkgroute.WriteNotModified(w)
		return
	}
	// Decided above, by 7.x's rule, so ServeContent must not decide again.
	r = pkgroute.WithoutHeaders(r, "If-None-Match", "If-Modified-Since")
	itemkit.ServeContent(&rangeRefusal{RangeRefusalWriter: pkgroute.RangeRefusalWriter{
		ResponseWriter: w,
	}}, r, d.name, d.modified, bytes.NewReader(d.body))
}

// checksums are body's three digests, as 7.x records a stored file's.
func checksums(body []byte) kernel.Checksums {
	s1 := sha1.Sum(body) //nolint:gosec // a checksum header, not a security control
	s256 := sha256.Sum256(body)
	m5 := md5.Sum(body) //nolint:gosec // a checksum header, not a security control
	return kernel.Checksums{
		SHA1: hex.EncodeToString(s1[:]), SHA256: hex.EncodeToString(s256[:]),
		MD5: hex.EncodeToString(m5[:]),
	}
}

// rangeRefusal also drops Cache-Control from a 416: 7.x answers a range
// past the end before it sets any of the file's headers, no-store among
// them (RequestResponseHelper.sendBodyResponse, 7.161.26).
type rangeRefusal struct {
	pkgroute.RangeRefusalWriter
}

// WriteHeader drops Cache-Control from a 416 before it sends it.
func (w *rangeRefusal) WriteHeader(status int) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Del("Cache-Control")
	}
	w.RangeRefusalWriter.WriteHeader(status)
}
