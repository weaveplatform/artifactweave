package generic_test

// This file holds #1593: an archive entry downloaded as a file is, with its
// own checksums and the archive's time, ranges and conditional requests,
// the file name headers and a browser's view by the content browsing flag
// of the repository that holds the archive, and a count against the
// archive's downloads.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/md5"  //nolint:gosec // 7.x's X-Checksum-Md5 names the algorithm.
	"crypto/sha1" //nolint:gosec // 7.x's ETag and X-Checksum-Sha1 name the algorithm.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// entryFiles are the entries of the archives these tests deploy.
var entryFiles = map[string]string{
	"e/in.txt": "inside", "e/naïve.txt": "naive", "e/lib.pom": "<project/>",
}

func entryZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, body := range files {
		f, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func entryTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// entryWorld is browsingWorld with entryFiles zipped at libs-local and
// brows-local z/e.zip and tarred at libs-local z/e.tar.gz.
func entryWorld(t *testing.T) *harness.ItemWorld {
	t.Helper()
	w, _, _ := browsingWorld(t)
	for p, body := range map[string][]byte{
		"/artifactory/libs-local/z/e.zip":    entryZip(t, entryFiles),
		"/artifactory/brows-local/z/e.zip":   entryZip(t, entryFiles),
		"/artifactory/libs-local/z/e.tar.gz": entryTarGz(t, entryFiles),
	} {
		harness.WantStatus(t, w.Do(http.MethodPut, p, "admin", string(body)), http.StatusCreated)
	}
	return w
}

// TestAccArchiveEntryIsServedAsAFile: an entry of a zip or tar.gz, from the
// repository that holds it or through a virtual repository, is answered as
// a file's download: its own SHA-1 as the bare ETag, its SHA-1, SHA-256 and
// MD5 in X-Checksum-*, the archive's Last-Modified, Accept-Ranges; a range,
// several ranges and an If-Range; a 304 for its SHA-1 or a date not before
// the archive's; a 416 with Content-Range alone; and X-Artifactory-Filename
// and the item route's Content-Disposition by the flag of the repository
// that holds the archive, on a GET, a HEAD, a range and a 304. 7.161.26's
// DownloadServiceImpl answers the ZipEntryResource through
// RequestResponseHelper as it answers a file; its setBasicHeaders sends the
// entry's checksums and name and the archive's time, and its
// sendBodyResponse reads Range and If-Range. An entry was sent with
// Content-Type, Content-Length and X-Artifactory-Filename alone, and every
// Range and condition was ignored (#1593).
func TestAccArchiveEntryIsServedAsAFile(t *testing.T) {
	w := entryWorld(t)
	sha1Sum := sha1.Sum([]byte("inside")) //nolint:gosec // the header's algorithm
	sha256Sum := sha256.Sum256([]byte("inside"))
	md5Sum := md5.Sum([]byte("inside")) //nolint:gosec // the header's algorithm
	sha := hex.EncodeToString(sha1Sum[:])
	sums := map[string]string{
		"ETag": sha, "X-Checksum-Sha1": sha,
		"X-Checksum-Sha256": hex.EncodeToString(sha256Sum[:]),
		"X-Checksum-Md5":    hex.EncodeToString(md5Sum[:]),
	}
	attachment := named{"in.txt", `attachment; filename="in.txt"; filename*=UTF-8''in.txt`}
	for _, c := range []struct {
		archive, via string
		want         named
	}{
		{"libs-local/z/e.zip", "libs-local/z/e.zip", attachment},
		{"libs-local/z/e.tar.gz", "libs-local/z/e.tar.gz", attachment},
		{"brows-local/z/e.zip", "brows-local/z/e.zip", named{"in.txt", ""}},
		{"brows-local/z/e.zip", "brows-virt/z/e.zip", named{"in.txt", ""}},
	} {
		modified := w.Do(http.MethodHead, "/artifactory/"+c.archive, "reader", "").
			Header().
			Get("Last-Modified")
		entry := "/artifactory/" + c.via + "!/e/in.txt"
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.Do(method, entry, "reader", "")
			h := rec.Header()
			body := "inside"
			if method == http.MethodHead {
				body = ""
			}
			if rec.Code != http.StatusOK || rec.Body.String() != body || modified == "" ||
				h.Get("Last-Modified") != modified || h.Get("Accept-Ranges") != "bytes" ||
				h.Get("Content-Length") != "6" || h.Get("Content-Type") != "text/plain" {
				t.Errorf("%s %s: %d %q %v; want the archive's Last-Modified %q", method, entry,
					rec.Code, rec.Body, h, modified)
			}
			for k, v := range sums {
				if h.Get(k) != v {
					t.Errorf("%s %s: %s %q, want %q", method, entry, k, h.Get(k), v)
				}
			}
		}
		for _, q := range namedRequests(sha) {
			wantNamed(t, q.label+" "+entry, w.Do(q.method, entry, "reader", "", q.headers...),
				q.status, c.want)
		}
		for _, q := range []struct {
			label   string
			headers []string
			status  int
			body    string
		}{
			{"range", []string{"Range", "bytes=1-2"}, http.StatusPartialContent, "ns"},
			{"If-Range the SHA-1", []string{"Range", "bytes=1-2", "If-Range", sha}, http.StatusPartialContent, "ns"},
			{"If-Range another tag", []string{"Range", "bytes=1-2", "If-Range", `"other"`}, http.StatusOK, "inside"},
			{"another tag", []string{"If-None-Match", `"other"`}, http.StatusOK, "inside"},
			{"the archive's date", []string{"If-Modified-Since", modified}, http.StatusNotModified, ""},
			{"416", []string{"Range", "bytes=100-"}, http.StatusRequestedRangeNotSatisfiable, ""},
		} {
			rec := w.Do(http.MethodGet, entry, "reader", "", q.headers...)
			if rec.Code != q.status || rec.Body.String() != q.body {
				t.Errorf(
					"%s %s: %d %q, want %d %q",
					q.label,
					entry,
					rec.Code,
					rec.Body,
					q.status,
					q.body,
				)
			}
			// A 304 beside an ETag sends no Last-Modified, as a file's.
			if q.status == http.StatusNotModified && (rec.Header().Get("ETag") != sha ||
				rec.Header().Get("Last-Modified") != "") {
				t.Errorf("%s %s: 304 headers %v", q.label, entry, rec.Header())
			}
			if q.status == http.StatusRequestedRangeNotSatisfiable &&
				(rec.Header().Get("Content-Range") != "bytes */6" ||
					rec.Header().Get("X-Artifactory-Filename") != "" || rec.Header().Get("ETag") != "") {
				t.Errorf("%s %s: 416 headers %v", q.label, entry, rec.Header())
			}
		}
		// Ranges that run backwards read the entry again from its start.
		rec := w.Do(http.MethodGet, entry, "reader", "", "Range", "bytes=3-4,0-1")
		if got := entryParts(t, rec); rec.Code != http.StatusPartialContent ||
			strings.Join(got, ",") != "id,in" {
			t.Errorf("ranges %s: %d %q", entry, rec.Code, got)
		}
	}
}

// entryParts are the bodies of rec's multipart/byteranges answer, in
// order, none when it is not one.
func entryParts(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	media, params, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	if err != nil || media != "multipart/byteranges" {
		return nil
	}
	var parts []string
	mr := multipart.NewReader(bytes.NewReader(rec.Body.Bytes()), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(b))
	}
}

// TestAccArchiveEntryNamesByTheArchivesRepository: an entry named outside
// printable ASCII is named by filename* alone, as an attachment from a
// repository without content browsing and inline, but for a 304, from one
// with it; a browser's GET of an entry from a repository with the flag,
// directly or through a virtual repository, gets Content-Security-Policy:
// sandbox and a .pom application/xml, and a range it cannot satisfy keeps
// the policy; a HEAD and a repository without the flag get neither.
// 7.161.26's setBasicHeaders and getModifiedMimeType read the response's
// repository, the one that holds the archive, and name the entry. 7.x's
// setHeaderForNonAscii reads the archive's name for an entry, which is the
// file the entry is not; the entry is named here [I] (#1593).
func TestAccArchiveEntryNamesByTheArchivesRepository(t *testing.T) {
	w := entryWorld(t)
	const naive = "na%C3%AFve.txt"
	future := "Wed, 21 Oct 2099 07:28:00 GMT"
	for _, c := range []struct {
		via               string
		want, notModified named
	}{
		{"libs-local", named{naive, "attachment; filename*=UTF-8''" + naive}, named{naive, "attachment; filename*=UTF-8''" + naive}},
		{"brows-local", named{naive, "inline; filename*=UTF-8''" + naive}, named{naive, ""}},
		{"brows-virt", named{naive, "inline; filename*=UTF-8''" + naive}, named{naive, ""}},
	} {
		entry := "/artifactory/" + c.via + "/z/e.zip!/e/" + naive
		wantNamed(t, "GET "+entry, w.Do(http.MethodGet, entry, "reader", ""), http.StatusOK, c.want)
		wantNamed(
			t,
			"HEAD "+entry,
			w.Do(http.MethodHead, entry, "reader", ""),
			http.StatusOK,
			c.want,
		)
		wantNamed(
			t,
			"304 "+entry,
			w.Do(http.MethodGet, entry, "reader", "", "If-Modified-Since", future),
			http.StatusNotModified,
			c.notModified,
		)
	}
	for _, c := range []struct {
		label, method, via string
		headers            []string
		status             int
		csp, contentType   string
	}{
		{"GET", http.MethodGet, "brows-local", nil, 200, "sandbox", "application/xml"},
		{"virtual GET", http.MethodGet, "brows-virt", nil, 200, "sandbox", "application/xml"},
		{"416", http.MethodGet, "brows-local", []string{"Range", "bytes=900-"}, 416, "sandbox", ""},
		{"HEAD", http.MethodHead, "brows-local", nil, 200, "", "application/x-maven-pom+xml"},
		{"no flag", http.MethodGet, "libs-local", nil, 200, "", "application/x-maven-pom+xml"},
	} {
		entry := "/artifactory/" + c.via + "/z/e.zip!/e/lib.pom"
		rec := w.Do(
			c.method,
			entry,
			"reader",
			"",
			append([]string{"User-Agent", browserAgent}, c.headers...)...)
		if h := rec.Header(); rec.Code != c.status || h.Get("Content-Security-Policy") != c.csp ||
			h.Get("Content-Type") != c.contentType {
			t.Errorf("%s %s: %d, Content-Security-Policy %q, Content-Type %q; want %d, %q, %q",
				c.label, entry, rec.Code, h.Get("Content-Security-Policy"), h.Get("Content-Type"),
				c.status, c.csp, c.contentType)
		}
	}
}

// TestAccArchiveEntryCountsAsADownloadOfTheArchive: a GET of an entry,
// directly or through a virtual repository, counts as a download of the
// archive that holds it, as a GET of the archive does; a HEAD, a 304 and
// skipUpdateStats do not. 7.161.26's DbStoringRepoMixin.getResourceStreamHandle
// updates the archive file's statistics for the entry's handle as for the
// archive's own. An entry was never counted (#1593).
func TestAccArchiveEntryCountsAsADownloadOfTheArchive(t *testing.T) {
	w := entryWorld(t)
	count := func() float64 {
		t.Helper()
		rec := w.Do(
			http.MethodGet,
			"/artifactory/api/storage/brows-local/z/e.zip?stats",
			"admin",
			"",
		)
		harness.WantStatus(t, rec, http.StatusOK)
		var stats map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
			t.Fatal(err)
		}
		n, _ := stats["downloadCount"].(float64)
		return n
	}
	sha := w.Do(http.MethodHead, "/artifactory/brows-local/z/e.zip!/e/in.txt", "reader", "").
		Header().Get("X-Checksum-Sha1")
	for _, c := range []struct {
		label, method, path string
		headers             []string
		want                float64
	}{
		{"GET", http.MethodGet, "brows-local/z/e.zip!/e/in.txt", nil, 1},
		{"virtual GET", http.MethodGet, "brows-virt/z/e.zip!/e/in.txt", nil, 2},
		{"HEAD", http.MethodHead, "brows-local/z/e.zip!/e/in.txt", nil, 2},
		{"304", http.MethodGet, "brows-local/z/e.zip!/e/in.txt", []string{"If-None-Match", sha}, 2},
		{"skipUpdateStats", http.MethodGet, "brows-local/z/e.zip!/e/in.txt?skipUpdateStats=true", nil, 2},
		{"the archive", http.MethodGet, "brows-local/z/e.zip", nil, 3},
	} {
		w.Do(c.method, "/artifactory/"+c.path, "reader", "", c.headers...)
		if got := count(); got != c.want {
			t.Errorf("after the %s: downloadCount %v, want %v", c.label, got, c.want)
		}
	}
}

// TestAccArchiveEntryTooCorruptToReadIsNotFound: an entry whose bytes fail
// the archive's own check answers 404, GET and HEAD, as an entry of a
// corrupt archive does, its neighbours still served. The entry is read for
// its checksums before anything is sent, as 7.x computes them
// (ZipEntryResourceImpl), and RTFACT-24360 shows 7.x answering 404 for an
// entry its archive's stream ends before [I]. It was answered 200 and a
// body that failed part way (#1593).
func TestAccArchiveEntryTooCorruptToReadIsNotFound(t *testing.T) {
	w := entryWorld(t)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, body := range map[string]string{"bad.txt": "corrupted", "good.txt": "intact"} {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	// Stored, the entry's bytes are its content: changing them fails its
	// CRC-32 when it has been read through.
	data := bytes.Replace(b.Bytes(), []byte("corrupted"), []byte("CORRUPTED"), 1)
	harness.WantStatus(
		t,
		w.Do(http.MethodPut, "/artifactory/libs-local/z/bad.zip", "admin", string(data)),
		http.StatusCreated,
	)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		harness.WantStatus(
			t,
			w.Do(method, "/artifactory/libs-local/z/bad.zip!/bad.txt", "reader", ""),
			http.StatusNotFound,
		)
		harness.WantStatus(
			t,
			w.Do(method, "/artifactory/libs-local/z/bad.zip!/good.txt", "reader", ""),
			http.StatusOK,
		)
	}
}
