package generic_test

// This file holds the conditional download suites of #1314. A file download
// answers If-None-Match, If-Modified-Since and If-Range, and ignores
// If-Match and If-Unmodified-Since, which Artifactory's download never
// evaluates: 6.20 decides a download's conditional answer in
// DownloadServiceImpl.isNotModified from If-Modified-Since and
// If-None-Match alone, and JFrog deferred RTFACT-17259, which asked for the
// other two. It holds on the item route, on a protocol route that serves
// the item download, on the registry's blob GET, on a redirecting
// repository and on a remote repository, streamed or cached.

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

// condAnswer is a conditional download's status, body and Content-Range.
type condAnswer struct {
	status       int
	body         string
	contentRange string
}

// conditional is a set of request headers and the answers a GET and a HEAD
// of the file give it.
type conditional struct {
	label     string
	headers   []string
	get, head condAnswer
}

// fileConditionals are the conditional requests of a file whose content is
// data, whose SHA-1 is sha and which last changed at modified: those naming
// If-Match or If-Unmodified-Since, which answer as the plain request does,
// and those the download evaluates, which answer as they did before #1314.
func fileConditionals(data []byte, sha string, modified time.Time) []conditional {
	quoted := `"` + sha + `"`
	before := modified.Add(-time.Hour).UTC().Format(http.TimeFormat)
	after := modified.Add(time.Hour).UTC().Format(http.TimeFormat)
	whole := condAnswer{http.StatusOK, string(data), ""}
	head := condAnswer{http.StatusOK, "", ""}
	notModified := condAnswer{http.StatusNotModified, "", ""}
	part := condAnswer{
		http.StatusPartialContent, string(data[2:6]),
		"bytes 2-5/" + strconv.Itoa(len(data)),
	}
	return []conditional{
		// Ignored, whatever they name.
		{"If-Match bare", []string{"If-Match", sha}, whole, head},
		{"If-Match quoted", []string{"If-Match", quoted}, whole, head},
		{"If-Match other", []string{"If-Match", `"other"`}, whole, head},
		{"If-Match any", []string{"If-Match", "*"}, whole, head},
		{"If-Unmodified-Since before", []string{"If-Unmodified-Since", before}, whole, head},
		{"If-Unmodified-Since after", []string{"If-Unmodified-Since", after}, whole, head},
		{
			"If-Match other, Range",
			[]string{"If-Match", `"other"`, "Range", "bytes=2-5"},
			part,
			head,
		},
		{
			"If-Match other, If-None-Match bare",
			[]string{"If-Match", `"other"`, "If-None-Match", sha},
			notModified, notModified,
		},
		{
			"If-Unmodified-Since before, If-Modified-Since after",
			[]string{"If-Unmodified-Since", before, "If-Modified-Since", after},
			notModified, notModified,
		},
		// Evaluated as before.
		{"If-None-Match bare", []string{"If-None-Match", sha}, notModified, notModified},
		{"If-None-Match quoted", []string{"If-None-Match", quoted}, notModified, notModified},
		{"If-None-Match other", []string{"If-None-Match", `"other"`}, whole, head},
		{"If-Modified-Since after", []string{"If-Modified-Since", after}, notModified, notModified},
		{"If-Modified-Since before", []string{"If-Modified-Since", before}, whole, head},
		// If-None-Match takes precedence over If-Modified-Since, as RFC 9110
		// has it and 7.125.6 does (RTDEV-64461).
		{
			"If-None-Match bare, If-Modified-Since before",
			[]string{"If-None-Match", sha, "If-Modified-Since", before},
			notModified, notModified,
		},
		{"If-Range bare", []string{"Range", "bytes=2-5", "If-Range", sha}, part, head},
		{"If-Range other", []string{"Range", "bytes=2-5", "If-Range", `"other"`}, whole, head},
	}
}

// wantCondAnswer fails t unless rec is want. A file's answer, 304
// included, carries its bare SHA-1 as ETag unless etag is empty.
func wantCondAnswer(
	t *testing.T,
	label string,
	rec *httptest.ResponseRecorder,
	want condAnswer,
	etag string,
) {
	t.Helper()
	h := rec.Header()
	if rec.Code != want.status || rec.Body.String() != want.body ||
		h.Get("Content-Range") != want.contentRange || h.Get("ETag") != etag {
		t.Errorf("%s: %d, ETag %q, Content-Range %q, body %q", label,
			rec.Code, h.Get("ETag"), h.Get("Content-Range"), rec.Body)
		t.Errorf("%s: want %d, ETag %q, Content-Range %q, body of %d bytes", label,
			want.status, etag, want.contentRange, len(want.body))
	}
}

// TestAccFileDownloadIgnoresIfMatchAndIfUnmodifiedSince: a file on a local
// and a virtual repository's item route, and a box on the item route and on
// the Vagrant box file route, which serves the item download, answer
// If-Match and If-Unmodified-Since as the plain request, GET and HEAD
// alike, and the other conditional headers as before.
func TestAccFileDownloadIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	w, data, sha := downloadWorld(t)
	modified := w.Item("libs-local", false, "dir/file.bin").Modified
	for _, path := range []string{
		"/artifactory/libs-local/dir/file.bin",
		"/artifactory/virt/dir/file.bin",
		"/artifactory/boxes/acme/focal-1.0.box",
		"/artifactory/api/vagrant/boxes/acme/focal-1.0.box",
	} {
		for _, c := range fileConditionals(data, sha, modified) {
			wantCondAnswer(t, "GET "+path+" "+c.label,
				w.Do(http.MethodGet, path, "reader", "", c.headers...), c.get, sha)
			wantCondAnswer(t, "HEAD "+path+" "+c.label,
				w.Do(http.MethodHead, path, "reader", "", c.headers...), c.head, sha)
		}
	}
}

// TestAccRedirectedFileIgnoresIfMatchAndIfUnmodifiedSince: a repository
// that redirects downloads answers a conditional GET itself (decision
// 0029), and answers it as a streamed download does: what streams 200 or
// 206 there redirects here, without an ETag, and a 304 stays local. A HEAD
// is never redirected.
func TestAccRedirectedFileIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	w, data, sha := downloadWorld(t)
	const p = "/artifactory/redirect-local/dir/file.bin"
	harness.WantStatus(t, w.Do(http.MethodPut, p, "admin", string(data)), http.StatusCreated)
	modified := w.Item("redirect-local", false, "dir/file.bin").Modified
	for _, c := range fileConditionals(data, sha, modified) {
		rec := w.Do(http.MethodGet, p, "reader", "", c.headers...)
		if c.get.status == http.StatusNotModified {
			wantCondAnswer(t, "GET "+c.label, rec, c.get, sha)
			if rec.Header().Get("Location") != "" {
				t.Errorf("GET %s: redirected to %s", c.label, rec.Header().Get("Location"))
			}
		} else {
			wantCondAnswer(t, "GET "+c.label, rec, condAnswer{http.StatusFound, "", ""}, "")
			if !strings.HasPrefix(rec.Header().Get("Location"), "https://storage.example/") {
				t.Errorf("GET %s: Location %q", c.label, rec.Header().Get("Location"))
			}
		}
		wantCondAnswer(t, "HEAD "+c.label,
			w.Do(http.MethodHead, p, "reader", "", c.headers...), c.head, sha)
	}
}

// TestAccRegistryBlobGetIgnoresIfMatchAndIfUnmodifiedSince: a layer GET
// serves the item download, so it answers If-Match and If-Unmodified-Since
// with the whole blob, as its HEAD already did (#1317), and If-None-Match
// with 304 as before.
func TestAccRegistryBlobGetIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	w := harness.NewItemWorld(t, false)
	if err := w.DB.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "docker-local", Class: repo.Local, PackageType: repo.Docker,
	})); err != nil {
		t.Fatal(err)
	}
	const layer = "a layer asked for on a condition"
	s256 := sha256.Sum256([]byte(layer))
	s1 := sha1.Sum([]byte(layer)) //nolint:gosec // the wire checksum
	hex256, sha := hex.EncodeToString(s256[:]), hex.EncodeToString(s1[:])
	digest := "sha256:" + hex256
	harness.WantStatus(t, w.Do(http.MethodPost,
		"/v2/docker-local/app/blobs/uploads/?digest="+digest, "admin", layer), http.StatusCreated)
	modified := w.Item("docker-local", false, "app/_uploads/sha256__"+hex256).Modified
	before := modified.Add(-time.Hour).UTC().Format(http.TimeFormat)
	whole := condAnswer{http.StatusOK, layer, ""}
	for _, c := range []conditional{
		{"If-Match bare", []string{"If-Match", sha}, whole, condAnswer{}},
		{"If-Match quoted", []string{"If-Match", `"` + sha + `"`}, whole, condAnswer{}},
		{"If-Match other", []string{"If-Match", `"other"`}, whole, condAnswer{}},
		{"If-Unmodified-Since before", []string{"If-Unmodified-Since", before}, whole, condAnswer{}},
		{"If-None-Match bare", []string{"If-None-Match", sha}, condAnswer{http.StatusNotModified, "", ""}, condAnswer{}},
	} {
		for _, base := range []string{"/v2/docker-local/app/blobs/", "/artifactory/api/docker/docker-local/v2/app/blobs/"} {
			wantCondAnswer(t, "GET "+base+" "+c.label,
				w.Do(http.MethodGet, base+digest, "admin", "", c.headers...), c.get, sha)
		}
	}
}

// TestAccRemoteFileDownloadIgnoresIfMatchAndIfUnmodifiedSince: a remote
// file, stored or not, on the first fetch and again, GET and HEAD, answers
// If-Match and If-Unmodified-Since as the plain request, whether the
// upstream's stream or the cached copy serves it, and If-Modified-Since as
// before.
func TestAccRemoteFileDownloadIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	modified := time.Unix(1700000000, 0).UTC()
	before := modified.Add(-time.Hour).UTC().Format(http.TimeFormat)
	after := modified.Add(time.Hour).UTC().Format(http.TimeFormat)
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			if !store {
				w.Repo.StoreArtifactsLocally = new(false)
				w.Update()
			}
			for i, c := range []struct {
				label   string
				headers func(sha string) []string
				plain   bool // answered as the plain request
			}{
				{"If-Match bare", func(sha string) []string { return []string{"If-Match", sha} }, true},
				{"If-Match quoted", func(sha string) []string { return []string{"If-Match", `"` + sha + `"`} }, true},
				{"If-Match other", func(string) []string { return []string{"If-Match", `"other"`} }, true},
				{"If-Unmodified-Since before", func(string) []string { return []string{"If-Unmodified-Since", before} }, true},
				{"If-Modified-Since before", func(string) []string { return []string{"If-Modified-Since", before} }, true},
				{"If-Modified-Since after", func(string) []string { return []string{"If-Modified-Since", after} }, false},
			} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					content := "a remote file on a condition " + strconv.Itoa(i) + method
					s := sha1.Sum([]byte(content)) //nolint:gosec // the wire checksum
					name := "/lib/c-" + strconv.Itoa(i) + method + ".bin"
					w.Upstream.Set(
						name,
						fakeupstream.Resource{Body: []byte(content), Modified: modified},
					)
					want := condAnswer{http.StatusNotModified, "", ""}
					if c.plain {
						want.status = http.StatusOK
						if method == http.MethodGet {
							want.body = content
						}
					}
					for _, pass := range []string{"first fetch", "again"} {
						rec := w.Request(method, "/artifactory/remote"+name, "reader", "",
							c.headers(hex.EncodeToString(s[:]))...)
						if rec.Code != want.status || rec.Body.String() != want.body {
							t.Errorf("%s %s %s %s: %d %q, want %d %q", pass, method, name, c.label,
								rec.Code, rec.Body, want.status, want.body)
						}
					}
				}
			}
		})
	}
}
