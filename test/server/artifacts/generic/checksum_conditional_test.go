package generic_test

// This file holds the conditional checksum suites of #1436 and #1362. A
// checksum file answers as 7.x's DownloadServiceImpl.respond answers one on
// every repository class (Artifactory OSS 7.161.26): its file's
// isNotModified decides first, so an If-None-Match naming the file's SHA-1
// (or its MD5, when X-Artifactory-If-None-Match-MD5 asks), or an
// If-Modified-Since the file has not changed since, answers 304 with the
// file's headers; If-Match and If-Unmodified-Since are never read; a HEAD
// answers 200 with the checksum's length and the file's headers whatever
// Range it sends; and a GET sends the checksum alone, dated now, with Range
// applied whatever If-Range says.

import (
	"crypto/md5"  //nolint:gosec // the wire checksum
	"crypto/sha1" //nolint:gosec // the wire checksum
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// sums are a file's checksums in hex.
type sums struct{ sha1, sha256, md5 string }

// fileSums returns the checksums of data.
func fileSums(data string) sums {
	s1 := sha1.Sum([]byte(data)) //nolint:gosec // the wire checksum
	s256 := sha256.Sum256([]byte(data))
	m := md5.Sum([]byte(data)) //nolint:gosec // the wire checksum
	return sums{hex.EncodeToString(s1[:]), hex.EncodeToString(s256[:]), hex.EncodeToString(m[:])}
}

// checksumConditionals are the conditional requests of a checksum file
// whose content is sum, of a file whose checksums are file and which last
// changed at modified, with the answers a GET and a HEAD give each.
func checksumConditionals(sum string, file sums, modified time.Time) []conditional {
	sha := file.sha1
	quoted := `"` + sha + `"`
	before := modified.Add(-time.Hour).UTC().Format(http.TimeFormat)
	after := modified.Add(time.Hour).UTC().Format(http.TimeFormat)
	whole := condAnswer{http.StatusOK, sum, ""}
	head := condAnswer{http.StatusOK, "", ""}
	notModified := condAnswer{http.StatusNotModified, "", ""}
	part := condAnswer{http.StatusPartialContent, sum[2:6], "bytes 2-5/" + strconv.Itoa(len(sum))}
	return []conditional{
		{"plain", nil, whole, head},
		// Never read.
		{"If-Match bare", []string{"If-Match", sha}, whole, head},
		{"If-Match quoted", []string{"If-Match", quoted}, whole, head},
		{"If-Match other", []string{"If-Match", `"other"`}, whole, head},
		{"If-Match any", []string{"If-Match", "*"}, whole, head},
		{"If-Unmodified-Since before", []string{"If-Unmodified-Since", before}, whole, head},
		// The file's isNotModified.
		{"If-Modified-Since before", []string{"If-Modified-Since", before}, whole, head},
		{"If-Modified-Since after", []string{"If-Modified-Since", after}, notModified, notModified},
		{"If-None-Match bare", []string{"If-None-Match", sha}, notModified, notModified},
		{"If-None-Match quoted", []string{"If-None-Match", quoted}, notModified, notModified},
		{"If-None-Match weak", []string{"If-None-Match", "W/" + quoted}, notModified, notModified},
		{"If-None-Match other", []string{"If-None-Match", `"other"`}, whole, head},
		{"If-None-Match any", []string{"If-None-Match", "*"}, whole, head},
		{"If-None-Match the MD5", []string{"If-None-Match", file.md5}, whole, head},
		{
			"If-None-Match the MD5, asked for",
			[]string{"If-None-Match", file.md5, "X-Artifactory-If-None-Match-MD5", "true"},
			notModified, notModified,
		},
		{
			"If-None-Match other, If-Modified-Since after",
			[]string{"If-None-Match", `"other"`, "If-Modified-Since", after},
			whole, head,
		},
		{
			"If-Match other, If-None-Match bare",
			[]string{"If-Match", `"other"`, "If-None-Match", sha},
			notModified, notModified,
		},
		// A GET's range, whatever If-Range says; a HEAD's whole length.
		{"Range", []string{"Range", "bytes=2-5"}, part, head},
		{
			"Range past the end",
			[]string{"Range", "bytes=1000-2000"},
			condAnswer{
				http.StatusRequestedRangeNotSatisfiable,
				"",
				"bytes */" + strconv.Itoa(len(sum)),
			},
			head,
		},
		{
			"Range, If-Range other",
			[]string{"Range", "bytes=2-5", "If-Range", `"other"`},
			part,
			head,
		},
		{"Range, If-Range before", []string{"Range", "bytes=2-5", "If-Range", before}, part, head},
	}
}

// wantChecksumAnswers fails t unless every checksumConditionals case on
// send, for the checksum file at target, answers as it should: a 304 and a
// HEAD with the file's headers (a HEAD with the checksum's length too), and
// a GET with none of them, dated no earlier than it was sent.
func wantChecksumAnswers(
	t *testing.T,
	target, sum string,
	file sums,
	send func(method string, headers ...string) *httptest.ResponseRecorder,
) {
	t.Helper()
	plain := send(http.MethodHead)
	modified, err := http.ParseTime(plain.Header().Get("Last-Modified"))
	if plain.Code != http.StatusOK || err != nil {
		t.Fatalf(
			"HEAD %s: %d, Last-Modified %q",
			target,
			plain.Code,
			plain.Header().Get("Last-Modified"),
		)
	}
	name := path.Base(target[:strings.LastIndex(target, ".")])
	fileHeaders := map[string]string{
		"Last-Modified":          plain.Header().Get("Last-Modified"),
		"X-Checksum-Sha1":        file.sha1,
		"X-Checksum-Sha256":      file.sha256,
		"X-Checksum-Md5":         file.md5,
		"X-Artifactory-Filename": name,
		"Accept-Ranges":          "bytes",
	}
	for _, c := range checksumConditionals(sum, file, modified) {
		label := target + " " + c.label
		start := time.Now().Truncate(time.Second)
		rec := send(http.MethodGet, c.headers...)
		if c.get.status == http.StatusNotModified {
			wantCondAnswer(t, "GET "+label, rec, c.get, file.sha1)
			wantHeaders(t, "GET "+label, rec, fileHeaders)
		} else {
			wantCondAnswer(t, "GET "+label, rec, c.get, "")
			wantGetHeaders(t, "GET "+label, rec, start)
		}
		rec = send(http.MethodHead, c.headers...)
		wantCondAnswer(t, "HEAD "+label, rec, c.head, file.sha1)
		wantHeaders(t, "HEAD "+label, rec, fileHeaders)
		if rec.Code == http.StatusOK {
			wantHeaders(t, "HEAD "+label, rec, map[string]string{
				"Content-Length": strconv.Itoa(len(sum)), "Content-Type": "application/x-checksum",
			})
		}
	}
}

// wantHeaders fails t unless rec carries each of want's headers with its
// value.
func wantHeaders(
	t *testing.T,
	label string,
	rec *httptest.ResponseRecorder,
	want map[string]string,
) {
	t.Helper()
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s: %s %q, want %q", label, k, got, v)
		}
	}
}

// wantGetHeaders fails t unless rec, a checksum GET sent at start, carries
// none of its file's headers, as 7.x's sendBodyResponseForChecksum sends
// none, and a Last-Modified no earlier than start.
func wantGetHeaders(t *testing.T, label string, rec *httptest.ResponseRecorder, start time.Time) {
	t.Helper()
	h := rec.Header()
	for _, k := range []string{"X-Checksum-Sha1", "X-Artifactory-Filename", "Accept-Ranges"} {
		if _, sent := h[k]; sent {
			t.Errorf("%s: %s %q sent", label, k, h.Get(k))
		}
	}
	if dated, err := http.ParseTime(h.Get("Last-Modified")); err != nil || dated.Before(start) {
		t.Errorf(
			"%s: Last-Modified %q, want no earlier than %s",
			label,
			h.Get("Last-Modified"),
			start,
		)
	}
}

// TestAccChecksumFileAnswersConditionalsAsTheDownload: a local and a
// virtual repository's checksum files answer If-None-Match and
// If-Modified-Since against their file, ignore If-Match and
// If-Unmodified-Since, and answer a HEAD's Range with the whole checksum,
// as a remote repository's do (#1436, #1362).
func TestAccChecksumFileAnswersConditionalsAsTheDownload(t *testing.T) {
	w := harness.NewItemWorld(t, false)
	const data = "a file whose checksums are asked for on a condition"
	file := fileSums(data)
	// libs-local keeps client checksums, so the deploy sends both.
	harness.WantStatus(
		t,
		w.Do(http.MethodPut, "/artifactory/libs-local/dir/sums.bin", "admin", data,
			"X-Checksum-Sha1", file.sha1, "X-Checksum-Sha256", file.sha256),
		http.StatusCreated,
	)
	for _, key := range []string{"libs-local", "virt"} {
		for ext, sum := range map[string]string{"sha1": file.sha1, "sha256": file.sha256} {
			target := "/artifactory/" + key + "/dir/sums.bin." + ext
			wantChecksumAnswers(t, target, sum, file,
				func(method string, headers ...string) *httptest.ResponseRecorder {
					return w.Do(method, target, "reader", "", headers...)
				})
		}
	}
}

// TestAccRemoteChecksumFileAnswersConditionalsAsTheDownload: a remote
// repository's checksum files answer as a local one's, whether the cache
// serves them (a generic remote, once its file is cached, and a Maven
// remote that stores) or the upstream's stream does (a Maven remote that
// stores nothing). The upstream sends the file's checksums as Artifactory
// does, so a HEAD that streams nothing still knows the file's SHA-1
// (#1436, #1362).
func TestAccRemoteChecksumFileAnswersConditionalsAsTheDownload(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	for _, store := range []bool{true, false} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
			Key: "maven-store-" + strconv.FormatBool(store), Class: repo.Remote,
			PackageType: repo.Maven, URL: w.Upstream.URL, StoreArtifactsLocally: new(store),
		})); err != nil {
			t.Fatal(err)
		}
	}
	const data = "a remote file whose checksums are asked for on a condition"
	file := fileSums(data)
	modified := time.Unix(1700000000, 0).UTC()
	headers := http.Header{
		"X-Checksum-Sha1": {
			file.sha1,
		},
		"X-Checksum-Sha256": {file.sha256},
		"X-Checksum-Md5":    {file.md5},
	}
	for _, name := range []string{"/dir/sums.bin", "/org/a/1.0/a-1.0.jar"} {
		w.Upstream.Set(
			name,
			fakeupstream.Resource{Body: []byte(data), Header: headers, Modified: modified},
		)
	}
	harness.WantStatus(
		t,
		w.Request(http.MethodGet, "/artifactory/remote/dir/sums.bin", "admin", ""),
		http.StatusOK,
	)
	for _, path := range []string{
		"/artifactory/remote/dir/sums.bin",
		"/artifactory/maven-store-true/org/a/1.0/a-1.0.jar",
		"/artifactory/maven-store-false/org/a/1.0/a-1.0.jar",
	} {
		for ext, sum := range map[string]string{"sha1": file.sha1, "sha256": file.sha256} {
			target := path + "." + ext
			wantChecksumAnswers(t, target, sum, file,
				func(method string, headers ...string) *httptest.ResponseRecorder {
					return w.Request(method, target, "admin", "", headers...)
				})
		}
	}
}

// TestAccColdRemoteChecksumAnswersConditionalsAsTheDownload: a SHA-256 a
// remote retrieves from its upstream without the file
// (retrieveSha256FromServer) ignores If-Match and If-Unmodified-Since and
// answers If-Modified-Since against the upstream checksum's time, GET and
// HEAD alike. Its file's checksums are unknown, so no If-None-Match
// matches, as 7.x's isNoneMatch treats a blank SHA-1, and a HEAD sends
// none of them (#1436).
func TestAccColdRemoteChecksumAnswersConditionalsAsTheDownload(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	w.Repo.RetrieveSha256FromServer = new(true)
	w.Update()
	digest := strings.Repeat("c", 64)
	modified := time.Unix(1700000000, 0).UTC()
	w.Upstream.Set("/cold.bin.sha256",
		fakeupstream.Resource{Body: []byte(digest + "  cold.bin\n"), Modified: modified})
	before := modified.Add(-time.Hour).Format(http.TimeFormat)
	after := modified.Add(time.Hour).Format(http.TimeFormat)
	whole := condAnswer{http.StatusOK, digest, ""}
	head := condAnswer{http.StatusOK, "", ""}
	notModified := condAnswer{http.StatusNotModified, "", ""}
	for _, c := range []conditional{
		{"If-Match other", []string{"If-Match", `"other"`}, whole, head},
		{"If-Unmodified-Since before", []string{"If-Unmodified-Since", before}, whole, head},
		{"If-Modified-Since after", []string{"If-Modified-Since", after}, notModified, notModified},
		{"If-Modified-Since before", []string{"If-Modified-Since", before}, whole, head},
		{"If-None-Match any", []string{"If-None-Match", "*"}, whole, head},
		{"If-None-Match the SHA-256", []string{"If-None-Match", digest}, whole, head},
		{
			"Range",
			[]string{"Range", "bytes=2-5", "If-Range", `"other"`},
			condAnswer{http.StatusPartialContent, digest[2:6], "bytes 2-5/64"},
			head,
		},
	} {
		for _, method := range []string{http.MethodHead, http.MethodGet} {
			want := c.get
			if method == http.MethodHead {
				want = c.head
			}
			wantCondAnswer(
				t,
				method+" "+c.label,
				w.Request(
					method,
					"/artifactory/remote/cold.bin.sha256",
					"reader",
					"",
					c.headers...),
				want,
				"",
			)
		}
	}
	rec := w.Request(http.MethodHead, "/artifactory/remote/cold.bin.sha256", "reader", "")
	if _, sent := rec.Header()["X-Checksum-Sha1"]; sent ||
		rec.Header().Get("X-Artifactory-Filename") != "cold.bin" {
		t.Errorf("HEAD: %v", rec.Header())
	}
}
