package artifacts_test

import (
	"crypto/sha1" //nolint:gosec // the wire checksum
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// headRanges are the Range headers a HEAD sends below for a file whose
// SHA-1 is sha1: a range, a suffix, one past the end, and a range under an
// If-Range naming the file.
func headRanges(sha1 string) [][]string {
	return [][]string{
		{"Range", "bytes=2-5"},
		{"Range", "bytes=-2"},
		{"Range", "bytes=1000-2000"},
		{"Range", "bytes=2-5", "If-Range", sha1},
		{"Range", "bytes=2-5", "If-Range", `"` + sha1 + `"`},
	}
}

// wantWholeHead fails t unless rec is a HEAD's 200 for content: its whole
// length, no Content-Range and no body, still offering byte ranges.
func wantWholeHead(t *testing.T, label string, rec *httptest.ResponseRecorder, content string) {
	t.Helper()
	h := rec.Header()
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 ||
		h.Get("Content-Length") != strconv.Itoa(len(content)) || h.Get("Content-Range") != "" ||
		h.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("%s: %d %q %v", label, rec.Code, rec.Body, h)
	}
}

// A HEAD on a file answers 200 with the whole length whatever Range it
// sends, on a local, redirecting, virtual and PyPI repository and through
// the PyPI API route alike: 6.20's sendHeadResponse never reads Range, and
// RFC 9110 section 14.2 lets a server ignore it on any method but GET [I].
// A GET still applies the range (#1338).
func TestAccItemHeadIgnoresRange(t *testing.T) {
	w := newItemWorld(t, false)
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI,
	})); err != nil {
		t.Fatal(err)
	}
	const content = "a file asked for in part"
	s := sha1.Sum([]byte(content)) //nolint:gosec // the wire checksum
	sum := hex.EncodeToString(s[:])
	for _, put := range []string{
		"/artifactory/libs-local/p/file.bin",
		"/artifactory/redirect-local/p/file.bin",
		"/artifactory/pypi-local/demo/demo-1.0.tar.gz",
	} {
		wantStatus(t, w.do(http.MethodPut, put, "admin", content), http.StatusCreated)
	}
	for _, path := range []string{
		"/artifactory/libs-local/p/file.bin",
		"/artifactory/redirect-local/p/file.bin",
		"/artifactory/virt/p/file.bin",
		"/artifactory/pypi-local/demo/demo-1.0.tar.gz",
		"/artifactory/api/pypi/pypi-local/demo/demo-1.0.tar.gz",
	} {
		for _, h := range headRanges(sum) {
			wantWholeHead(t, "HEAD "+path+" "+strings.Join(h, " "),
				w.do(http.MethodHead, path, "admin", "", h...), content)
		}
	}
	part := w.do(
		http.MethodGet,
		"/artifactory/libs-local/p/file.bin",
		"admin",
		"",
		"Range",
		"bytes=2-5",
	)
	if part.Code != http.StatusPartialContent || part.Body.String() != content[2:6] ||
		part.Header().Get("Content-Range") != "bytes 2-5/"+strconv.Itoa(len(content)) {
		t.Fatalf("GET Range: %d %q %v", part.Code, part.Body, part.Header())
	}
	wantStatus(t, w.do(http.MethodGet, "/artifactory/libs-local/p/file.bin", "admin", "",
		"Range", "bytes=1000-2000"), http.StatusRequestedRangeNotSatisfiable)
}

// A HEAD on a remote file, stored or not, on the first fetch and from the
// cache, answers 200 with the whole length whatever Range it sends, as a
// local file's does (#1338).
func TestAccRemoteItemHeadIgnoresRange(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			if !store {
				w.Repo.StoreArtifactsLocally = ptr(false)
				w.Update()
			}
			for i := range headRanges("") {
				content := "a remote file asked for in part " + strconv.Itoa(i)
				s := sha1.Sum([]byte(content)) //nolint:gosec // the wire checksum
				h := headRanges(hex.EncodeToString(s[:]))[i]
				name := "/lib/f-" + strconv.Itoa(i) + ".bin"
				w.Upstream.Set(name, fakeupstream.Resource{Body: []byte(content)})
				for _, pass := range []string{"first fetch", "cache"} {
					wantWholeHead(
						t,
						pass+" HEAD "+name+" "+strings.Join(h, " "),
						w.Request(
							http.MethodHead,
							"/artifactory/remote"+name,
							"reader",
							"",
							h...),
						content,
					)
				}
			}
		})
	}
}
