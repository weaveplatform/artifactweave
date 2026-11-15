package gems_test

import (
	"bytes"
	"crypto/md5"  //nolint:gosec // the checksum header and compact index ETag 7.x sends
	"crypto/sha1" //nolint:gosec // the checksum header and ETag 7.x sends
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	gemfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
)

// storedTypes are the Content-Types of the documents 7.x stores: versions
// as 7.41.13 typed it (RTFACT-27459), names and info/<gem> alike, and the
// spec index files and quick gemspecs by 7.x's MIME table [I].
var storedTypes = map[string]string{
	"names": "application/octet-stream", "versions": "application/octet-stream",
	"info/rake":               "application/octet-stream",
	"specs.4.8.gz":            "application/x-gzip",
	"latest_specs.4.8.gz":     "application/x-gzip",
	"prerelease_specs.4.8.gz": "application/x-gzip",
	"quick/Marshal.4.8/rake-12.3.1.gemspec.rz": "application/x-ruby-marshal",
}

// wantStoredDocument fails unless a GET and a HEAD of the document doc at
// base answer 200 with the headers 7.41.13 sent on GET
// /api/gems/<repo>/versions (RTFACT-27459): its type (storedTypes),
// Cache-Control: no-store, X-Artifactory-Filename, the quoted name alone as
// Content-Disposition, the three checksums of the bytes served,
// Last-Modified, Accept-Ranges and an ETag, the quoted MD5 for the compact
// index and the bare SHA-1 for a spec index file [I], with the Repr-Digest
// the route sent before. It returns the GET.
func (w *world) wantStoredDocument(base, doc string) *httptest.ResponseRecorder {
	w.t.Helper()
	get := w.do(http.MethodGet, base+doc, "admin", nil)
	status(w.t, get, http.StatusOK)
	body := get.Body.Bytes()
	s1, s256, m5 := sha1.Sum(
		body,
	), sha256.Sum256(
		body,
	), md5.Sum(
		body,
	) //nolint:gosec // checksum headers
	etag := hex.EncodeToString(s1[:])
	if compact := !strings.Contains(doc, ".") || strings.HasPrefix(doc, "info/"); compact {
		etag = `"` + hex.EncodeToString(m5[:]) + `"`
	}
	name := path.Base(doc)
	want := map[string]string{
		"Content-Type":           storedTypes[doc],
		"Cache-Control":          "no-store",
		"X-Artifactory-Filename": name,
		"Content-Disposition":    `attachment; filename="` + name + `"`,
		"Etag":                   etag,
		"X-Checksum-Sha1":        hex.EncodeToString(s1[:]),
		"X-Checksum-Sha256":      hex.EncodeToString(s256[:]),
		"X-Checksum-Md5":         hex.EncodeToString(m5[:]),
		"Accept-Ranges":          "bytes",
		"Content-Length":         strconv.Itoa(len(body)),
		"Repr-Digest":            `sha-256="` + base64.StdEncoding.EncodeToString(s256[:]) + `"`,
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := get
		if method == http.MethodHead {
			rec = w.do(method, base+doc, "admin", nil)
			status(w.t, rec, http.StatusOK)
			if rec.Body.Len() != 0 {
				w.t.Errorf("HEAD %s: %d bytes of body", doc, rec.Body.Len())
			}
		}
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				w.t.Errorf("%s %s%s: %s %q, want %q", method, base, doc, k, got, v)
			}
		}
		if _, err := http.ParseTime(rec.Header().Get("Last-Modified")); err != nil {
			w.t.Errorf("%s %s%s: Last-Modified %q", method, base, doc,
				rec.Header().Get("Last-Modified"))
		}
	}
	return get
}

// wantRevalidated fails unless the document doc at base, whose GET was get,
// answers If-None-Match with its ETag, as sent or bare, and with its SHA-1,
// with 304 carrying the ETag, as 7.x's download decides
// (DownloadServiceImpl.isNotModified, 7.161.26), and another tag with the
// document; a ranged GET with its ETag answers 206, as Bundler's append of
// versions asks. If-Modified-Since its own time answers the document,
// which is built afresh [I].
func (w *world) wantRevalidated(base, doc string, get *httptest.ResponseRecorder) {
	w.t.Helper()
	etag := get.Header().Get("ETag")
	sha1 := get.Header().Get("X-Checksum-Sha1")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, tag := range []string{etag, strings.Trim(etag, `"`), sha1} {
			rec := w.do(method, base+doc, "admin", nil, "If-None-Match", tag)
			if rec.Code != http.StatusNotModified || rec.Header().Get("ETag") != etag ||
				rec.Body.Len() != 0 {
				w.t.Errorf("%s %s%s If-None-Match %s: %d ETag %q, want 304 with %s", method, base,
					doc, tag, rec.Code, rec.Header().Get("ETag"), etag)
			}
		}
		if rec := w.do(method, base+doc, "admin", nil, "If-None-Match", `"other"`); rec.Code !=
			http.StatusOK {
			w.t.Errorf("%s %s%s If-None-Match other: %d, want 200", method, base, doc, rec.Code)
		}
		if rec := w.do(method, base+doc, "admin", nil, "If-Modified-Since",
			get.Header().Get("Last-Modified")); rec.Code != http.StatusOK {
			w.t.Errorf("%s %s%s If-Modified-Since its time: %d, want 200", method, base, doc,
				rec.Code)
		}
	}
	part := w.do(http.MethodGet, base+doc, "admin", nil, "Range", "bytes=1-", "If-Range", etag)
	if part.Code != http.StatusPartialContent ||
		!bytes.Equal(part.Body.Bytes(), get.Body.Bytes()[1:]) {
		w.t.Errorf("GET %s%s Range If-Range %s: %d, want 206 with the rest", base, doc, etag,
			part.Code)
	}
	past := w.do(http.MethodGet, base+doc, "admin", nil,
		"Range", "bytes="+strconv.Itoa(get.Body.Len()+10)+"-")
	if past.Code != http.StatusRequestedRangeNotSatisfiable || past.Body.Len() != 0 ||
		past.Header().Get("Cache-Control") != "" || past.Header().Get("ETag") != "" ||
		past.Header().Get("X-Artifactory-Filename") != "" {
		w.t.Errorf("GET %s%s Range past the end: %d %v, want 416 with Content-Range alone", base,
			doc, past.Code, past.Header())
	}
}

// TestAccGemsIndexAnswersAsAStoredFile: the compact index (names, versions,
// info/<gem>), the spec index files and quick gemspecs, GET and HEAD, from
// a local, a federated and a virtual repository and from a remote and a
// virtual repository over it, answer with a stored file's headers in the
// package download service's form. 7.41.13 answered GET
// /api/gems/<repo>/versions with X-Artifactory-Filename, Content-Disposition:
// attachment; filename="versions", the checksums, the MD5 quoted as the
// ETag, Last-Modified, Accept-Ranges, Cache-Control: no-store and
// application/octet-stream (RTFACT-27459); the gems API sent none of the
// file headers, Cache-Control: private, no-store and text/plain (#1582).
// The JSON API, which 7.x computes per request, keeps its answer.
func TestAccGemsIndexAnswersAsAStoredFile(t *testing.T) {
	t.Run("local, federated and virtual", func(t *testing.T) {
		w := newWorld(t)
		for _, key := range []string{"gems-local", "gems-fed"} {
			status(t, w.do(http.MethodPut, "/artifactory/"+key+"/gems/rake-12.3.1.gem", "admin",
				w.gem), http.StatusCreated)
		}
		for _, key := range []string{"gems-local", "gems-fed", "gems-virtual"} {
			base := "/artifactory/api/gems/" + key + "/"
			for doc := range storedTypes {
				w.wantRevalidated(base, doc, w.wantStoredDocument(base, doc))
			}
			api := w.do(http.MethodGet, base+"api/v1/gems/rake.json", "admin", nil)
			status(t, api, http.StatusOK)
			if api.Header().Get("Cache-Control") != "private, no-store" ||
				api.Header().Get("X-Artifactory-Filename") != "" ||
				api.Header().Get("Content-Type") != "application/json" {
				t.Errorf("%s JSON API: %v", key, api.Header())
			}
		}
	})

	t.Run("remote and virtual over it", func(t *testing.T) {
		up := fakeupstream.New(t, fakeupstream.Options{})
		w := newRemoteWorld(t, up)
		p, err := gemfmt.Read(t.Context(), bytes.NewReader(w.gem), int64(len(w.gem)),
			"gems/rake-12.3.1.gem")
		if err != nil {
			t.Fatal(err)
		}
		packages := []gemfmt.Package{p}
		up.Set("/versions", fakeupstream.Resource{
			Body:     gemfmt.CompactVersions(packages),
			Modified: time.Now().Add(-time.Hour),
		})
		up.Set("/info/rake", fakeupstream.Resource{Body: gemfmt.CompactInfo("rake", packages)})
		for _, key := range []string{"gems-remote", "gems-remote-virtual"} {
			base := "/artifactory/api/gems/" + key + "/"
			for _, doc := range []string{"names", "versions", "info/rake", "specs.4.8.gz"} {
				w.wantRevalidated(base, doc, w.wantStoredDocument(base, doc))
			}
		}
	})
}
