package generic_test

// This file holds the conditional checksum suites of #1436 and #1362. A
// checksum file answers conditional requests as 7.x's
// DownloadServiceImpl.respond answers one on every repository class
// (Artifactory OSS 7.161.26): its file's isNotModified decides first, so an
// If-None-Match naming the file's SHA-1, or an If-Modified-Since the file
// has not changed since, answers 304; If-Match and If-Unmodified-Since are
// never read; a HEAD answers 200 with the checksum's length whatever Range
// it sends; and a GET applies Range.

import (
	"crypto/sha1" //nolint:gosec // the wire checksum
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// checksumConditionals are the conditional requests of a checksum file
// whose content is sum, of a file whose SHA-1 is sha and which last changed
// at modified, with the answers a GET and a HEAD give each.
func checksumConditionals(sum, sha string, modified time.Time) []conditional {
	quoted := `"` + sha + `"`
	before := modified.Add(-time.Hour).UTC().Format(http.TimeFormat)
	after := modified.Add(time.Hour).UTC().Format(http.TimeFormat)
	whole := condAnswer{http.StatusOK, sum, ""}
	head := condAnswer{http.StatusOK, "", ""}
	notModified := condAnswer{http.StatusNotModified, "", ""}
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
		// A GET's range; a HEAD's whole length.
		{
			"Range",
			[]string{"Range", "bytes=2-5"},
			condAnswer{http.StatusPartialContent, sum[2:6], "bytes 2-5/" + strconv.Itoa(len(sum))},
			head,
		},
		{
			"Range past the end",
			[]string{"Range", "bytes=1000-2000"},
			condAnswer{
				http.StatusRequestedRangeNotSatisfiable, "invalid range: failed to overlap\n",
				"bytes */" + strconv.Itoa(len(sum)),
			},
			head,
		},
		{"Range, If-Range", []string{"Range", "bytes=2-5", "If-Range", `"other"`}, whole, head},
	}
}

// wantChecksumAnswers fails t unless every checksumConditionals case on
// send answers as it should: a 200 HEAD with the checksum's length, a 304
// without a body.
func wantChecksumAnswers(
	t *testing.T,
	label, sum, sha string,
	send func(method string, headers ...string) *httptest.ResponseRecorder,
) {
	t.Helper()
	plain := send(http.MethodGet)
	modified, err := http.ParseTime(plain.Header().Get("Last-Modified"))
	if plain.Code != http.StatusOK || plain.Body.String() != sum || err != nil {
		t.Fatalf("%s: %d %q, Last-Modified %q", label, plain.Code, plain.Body,
			plain.Header().Get("Last-Modified"))
	}
	for _, c := range checksumConditionals(sum, sha, modified) {
		wantCondAnswer(t, "GET "+label+" "+c.label, send(http.MethodGet, c.headers...), c.get, "")
		rec := send(http.MethodHead, c.headers...)
		wantCondAnswer(t, "HEAD "+label+" "+c.label, rec, c.head, "")
		if rec.Code == http.StatusOK &&
			rec.Header().Get("Content-Length") != strconv.Itoa(len(sum)) {
			t.Errorf("HEAD %s %s: Content-Length %q, want %d", label, c.label,
				rec.Header().Get("Content-Length"), len(sum))
		}
	}
}

// fileSums returns the SHA-1 and SHA-256 of data, in hex.
func fileSums(data string) (string, string) {
	s1 := sha1.Sum([]byte(data)) //nolint:gosec // the wire checksum
	s256 := sha256.Sum256([]byte(data))
	return hex.EncodeToString(s1[:]), hex.EncodeToString(s256[:])
}

// TestAccChecksumFileAnswersConditionalsAsTheDownload: a local and a
// virtual repository's checksum files answer If-None-Match and
// If-Modified-Since against their file, ignore If-Match and
// If-Unmodified-Since, and answer a HEAD's Range with the whole checksum,
// as a remote repository's do (#1436, #1362).
func TestAccChecksumFileAnswersConditionalsAsTheDownload(t *testing.T) {
	w := harness.NewItemWorld(t, false)
	const data = "a file whose checksums are asked for on a condition"
	sha, sum256 := fileSums(data)
	// libs-local keeps client checksums, so the deploy sends both.
	harness.WantStatus(
		t,
		w.Do(http.MethodPut, "/artifactory/libs-local/dir/sums.bin", "admin", data,
			"X-Checksum-Sha1", sha, "X-Checksum-Sha256", sum256),
		http.StatusCreated,
	)
	for _, key := range []string{"libs-local", "virt"} {
		for ext, sum := range map[string]string{"sha1": sha, "sha256": sum256} {
			path := "/artifactory/" + key + "/dir/sums.bin." + ext
			wantChecksumAnswers(t, path, sum, sha,
				func(method string, headers ...string) *httptest.ResponseRecorder {
					return w.Do(method, path, "reader", "", headers...)
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
	sha, sum256 := fileSums(data)
	modified := time.Unix(1700000000, 0).UTC()
	sums := http.Header{"X-Checksum-Sha1": {sha}, "X-Checksum-Sha256": {sum256}}
	for _, name := range []string{"/dir/sums.bin", "/org/a/1.0/a-1.0.jar"} {
		w.Upstream.Set(
			name,
			fakeupstream.Resource{Body: []byte(data), Header: sums, Modified: modified},
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
		for ext, sum := range map[string]string{"sha1": sha, "sha256": sum256} {
			target := path + "." + ext
			wantChecksumAnswers(t, target, sum, sha,
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
// HEAD alike. Its file's SHA-1 is unknown, so no If-None-Match matches, as
// 7.x's isNoneMatch treats a blank SHA-1 (#1436).
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
		{"Range", []string{"Range", "bytes=2-5"}, condAnswer{http.StatusPartialContent, digest[2:6], "bytes 2-5/64"}, head},
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
}
