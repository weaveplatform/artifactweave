package itemkit

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// downloadRequest drops If-Match and If-Unmodified-Since from every
// request, and Range and If-Range from a HEAD only, leaving the caller's
// request as it was sent.
func TestUnitDownloadRequest(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, "/artifactory/libs-local/a.bin", nil)
		for _, k := range []string{
			"If-Match", "If-Unmodified-Since", "Range", "If-Range", "If-None-Match", "If-Modified-Since",
		} {
			r.Header.Set(k, k)
		}
		got := downloadRequest(r)
		for _, gone := range unevaluatedPreconditions {
			if got.Header.Get(gone) != "" {
				t.Errorf("%s: %s kept", method, gone)
			}
		}
		for _, k := range []string{"Range", "If-Range"} {
			if kept := got.Header.Get(k) != ""; kept != (method == http.MethodGet) {
				t.Errorf("%s: %s kept is %t", method, k, kept)
			}
		}
		for _, k := range []string{"If-None-Match", "If-Modified-Since"} {
			if got.Header.Get(k) != k {
				t.Errorf("%s: %s dropped", method, k)
			}
		}
		if len(r.Header) != 6 {
			t.Errorf("%s: the caller's request changed: %v", method, r.Header)
		}
	}
	plain := httptest.NewRequest(http.MethodHead, "/artifactory/libs-local/a.bin", nil)
	if downloadRequest(plain) != plain {
		t.Error("a request without the headers was copied")
	}
}

// contentAnswer is a response's status, body and Content-Range.
type contentAnswer struct {
	status       int
	body         string
	contentRange string
}

func serveContentAnswer(method, etag string, modified time.Time, headers ...string) contentAnswer {
	r := httptest.NewRequest(method, "/api/x/index", nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	if etag != "" {
		rec.Header().Set("ETag", etag)
	}
	ServeContent(rec, r, "index", modified, bytes.NewReader([]byte("generated index")))
	return contentAnswer{rec.Code, rec.Body.String(), rec.Header().Get("Content-Range")}
}

// ServeContent answers If-Match and If-Unmodified-Since as the plain
// request, and a HEAD with Range with the whole length, with or without an
// ETag or a modified time; it answers If-None-Match, If-Modified-Since and
// a GET's Range as http.ServeContent does.
func TestUnitServeContentAnswersAsTheDownload(t *testing.T) {
	modified := time.Unix(1700000000, 0).UTC()
	before := modified.Add(-time.Hour).Format(http.TimeFormat)
	after := modified.Add(time.Hour).Format(http.TimeFormat)
	whole := contentAnswer{http.StatusOK, "generated index", ""}
	head := contentAnswer{http.StatusOK, "", ""}
	notModified := contentAnswer{http.StatusNotModified, "", ""}
	part := contentAnswer{http.StatusPartialContent, "nera", "bytes 2-5/15"}
	for _, c := range []struct {
		label     string
		etag      string
		modified  time.Time
		headers   []string
		get, head contentAnswer
	}{
		{"If-Match other", "", time.Time{}, []string{"If-Match", `"other"`}, whole, head},
		{"If-Match other, ETag", `"e"`, modified, []string{"If-Match", `"other"`}, whole, head},
		{"If-Unmodified-Since before", "", modified, []string{"If-Unmodified-Since", before}, whole, head},
		{"Range", "", time.Time{}, []string{"Range", "bytes=2-5"}, part, head},
		{
			"Range past the end", "",
			time.Time{},
			[]string{"Range", "bytes=1000-2000"},
			contentAnswer{http.StatusRequestedRangeNotSatisfiable, "invalid range: failed to overlap\n", "bytes */15"},
			head,
		},
		{"Range, If-Range", `"e"`, time.Time{}, []string{"Range", "bytes=2-5", "If-Range", `"e"`}, part, head},
		{"If-None-Match", `"e"`, time.Time{}, []string{"If-None-Match", `"e"`}, notModified, notModified},
		{"If-None-Match any", "", time.Time{}, []string{"If-None-Match", "*"}, notModified, notModified},
		{"If-Modified-Since after", "", modified, []string{"If-Modified-Since", after}, notModified, notModified},
		{"If-Modified-Since, no time", "", time.Time{}, []string{"If-Modified-Since", after}, whole, head},
	} {
		if got := serveContentAnswer(
			http.MethodGet,
			c.etag,
			c.modified,
			c.headers...); got != c.get {
			t.Errorf("GET %s: %+v, want %+v", c.label, got, c.get)
		}
		if got := serveContentAnswer(
			http.MethodHead,
			c.etag,
			c.modified,
			c.headers...); got != c.head {
			t.Errorf("HEAD %s: %+v, want %+v", c.label, got, c.head)
		}
	}
}

// fileNotModified decides as 7.x's isNotModified: a header line of
// If-None-Match that is the SHA-1, bare, quoted or weak, matches, and
// decides alone; "*" and a list on one line do not match; otherwise
// If-Modified-Since matches a file unchanged since, to the second.
func TestUnitFileNotModified(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	modified := time.Unix(1700000000, 900_000_000).UTC()
	same := modified.Truncate(time.Second).Format(http.TimeFormat)
	before := modified.Add(-time.Hour).Format(http.TimeFormat)
	after := modified.Add(time.Hour).Format(http.TimeFormat)
	for _, c := range []struct {
		label    string
		sha1     string
		modified time.Time
		headers  [][2]string
		want     bool
	}{
		{"none", sha, modified, nil, false},
		{"bare", sha, modified, [][2]string{{"If-None-Match", sha}}, true},
		{"quoted", sha, modified, [][2]string{{"If-None-Match", `"` + sha + `"`}}, true},
		{"weak", sha, modified, [][2]string{{"If-None-Match", `W/"` + sha + `"`}}, true},
		{"spaced", sha, modified, [][2]string{{"If-None-Match", " " + sha + " "}}, true},
		{"other", sha, modified, [][2]string{{"If-None-Match", `"other"`}}, false},
		{"any", sha, modified, [][2]string{{"If-None-Match", "*"}}, false},
		{"list on one line", sha, modified, [][2]string{{"If-None-Match", `"other", "` + sha + `"`}}, false},
		{"second line", sha, modified, [][2]string{{"If-None-Match", `"other"`}, {"If-None-Match", sha}}, true},
		{"no SHA-1", "", modified, [][2]string{{"If-None-Match", `""`}}, false},
		{
			"If-None-Match decides alone", sha, modified,
			[][2]string{{"If-None-Match", `"other"`}, {"If-Modified-Since", after}},
			false,
		},
		{
			"If-None-Match over an earlier If-Modified-Since", sha, modified,
			[][2]string{{"If-None-Match", sha}, {"If-Modified-Since", before}},
			true,
		},
		{"If-Modified-Since after", sha, modified, [][2]string{{"If-Modified-Since", after}}, true},
		{"If-Modified-Since the same second", sha, modified, [][2]string{{"If-Modified-Since", same}}, true},
		{"If-Modified-Since before", sha, modified, [][2]string{{"If-Modified-Since", before}}, false},
		{"If-Modified-Since not a date", sha, modified, [][2]string{{"If-Modified-Since", "soon"}}, false},
		{"If-Modified-Since, no time", sha, time.Time{}, [][2]string{{"If-Modified-Since", after}}, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/a.bin.sha1", nil)
		for _, h := range c.headers {
			r.Header.Add(h[0], h[1])
		}
		if got := fileNotModified(r, c.sha1, c.modified); got != c.want {
			t.Errorf("%s: %t, want %t", c.label, got, c.want)
		}
	}
}

// serveChecksum answers 304 with the file's Last-Modified and no body or
// type when the file is unchanged, ignores If-Match and If-Unmodified-Since,
// applies a GET's Range and answers a HEAD's with the whole length.
func TestUnitServeChecksum(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	modified := time.Unix(1700000000, 0).UTC()
	serve := func(method string, sum string, at time.Time, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/libs-local/a.bin.sha1", nil)
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		serveChecksum(rec, r, "a.bin.sha1", sha, sum, at)
		return rec
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := serve(method, sha, modified, "If-None-Match", sha)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 ||
			rec.Header().Get("Last-Modified") != modified.Format(http.TimeFormat) ||
			rec.Header().Get("Content-Type") != "" {
			t.Errorf("%s If-None-Match: %d %v %q", method, rec.Code, rec.Header(), rec.Body)
		}
		rec = serve(method, sha, time.Time{}, "If-None-Match", sha)
		if rec.Code != http.StatusNotModified || rec.Header().Get("Last-Modified") != "" {
			t.Errorf("%s If-None-Match, no time: %d %v", method, rec.Code, rec.Header())
		}
		for _, h := range [][]string{
			{"If-Match", `"other"`},
			{"If-Unmodified-Since", modified.Add(-time.Hour).Format(http.TimeFormat)},
			{"If-None-Match", sha},
		} {
			sum := sha
			if h[0] == "If-None-Match" {
				sum = "" // the file's SHA-1 is not known
			}
			rec := serve(method, sum, modified, h...)
			want := sha
			if method == http.MethodHead {
				want = ""
			}
			if rec.Code != http.StatusOK || rec.Body.String() != want ||
				rec.Header().Get("Content-Length") != "40" {
				t.Errorf("%s %v: %d %v %q", method, h, rec.Code, rec.Header(), rec.Body)
			}
		}
	}
	if rec := serve(
		http.MethodGet,
		sha,
		modified,
		"Range",
		"bytes=2-5",
	); rec.Code != http.StatusPartialContent ||
		rec.Body.String() != sha[2:6] ||
		rec.Header().Get("Content-Range") != "bytes 2-5/40" {
		t.Errorf("GET Range: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if rec := serve(
		http.MethodHead,
		sha,
		modified,
		"Range",
		"bytes=2-5",
	); rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Range") != "" ||
		rec.Header().Get("Content-Length") != "40" {
		t.Errorf("HEAD Range: %d %v", rec.Code, rec.Header())
	}
}

// directServeContent is the call a package type's handler must not make:
// http.ServeContent answers If-Match and If-Unmodified-Since with 412 and a
// HEAD's Range with 206 or 416, which Artifactory's download never does.
const directServeContent = "http.ServeContent("

// directServeContentAllowed are the ecosystem directories whose handlers
// may still call http.ServeContent themselves: the registry answers its
// manifests' and blobs' preconditions by the Docker API's rules, which
// #1437 settles.
var directServeContentAllowed = []string{"internal/artifacts/ecosystem/docker/"}

// No package type's handler serves content through http.ServeContent
// directly, so every index and metadata document a route builds answers
// conditional requests and a HEAD's Range through ServeContent, as the
// download does (#1438, #1362).
func TestUnitEcosystemHandlersServeThroughServeContent(t *testing.T) {
	root := repopath.Root(t)
	var found []string
	err := filepath.WalkDir(filepath.Join(root, "internal", "artifacts", "ecosystem"),
		func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
			for _, allowed := range directServeContentAllowed {
				if strings.HasPrefix(rel, allowed) {
					return nil
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err //nolint:wrapcheck // the walk's own error
			}
			if bytes.Contains(data, []byte(directServeContent)) {
				found = append(found, rel)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range found {
		t.Errorf("%s calls %s; serve through itemkit.ServeContent", rel, directServeContent)
	}
}
