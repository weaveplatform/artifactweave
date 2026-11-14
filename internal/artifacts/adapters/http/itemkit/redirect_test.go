package itemkit

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func TestAccThrottle(t *testing.T) {
	start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	th := newThrottle(time.Minute)
	for _, c := range []struct {
		after time.Duration
		ok    bool
		held  int
	}{
		{0, true, 0},
		{time.Second, false, 0},
		{59 * time.Second, false, 0},
		{time.Minute, true, 2},
		{90 * time.Second, false, 0},
		{3 * time.Minute, true, 1},
	} {
		if ok, held := th.allow(start.Add(c.after)); ok != c.ok || held != c.held {
			t.Fatalf("at %v: allow %v held %d, want %v %d", c.after, ok, held, c.ok, c.held)
		}
	}
	var none *throttle
	if ok, _ := none.allow(start); !ok {
		t.Fatal("a nil throttle holds nothing back")
	}
}

// TestUnitMinRedirectSizeReadsTheDescriptor: the smallest file that
// redirects is the descriptor's size once one is applied, and the
// configured size when there is no descriptor, it holds no size, or it
// cannot be read, which is logged.
func TestUnitMinRedirectSizeReadsTheDescriptor(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	for _, tc := range []struct {
		name   string
		read   func(context.Context) (int64, bool, error)
		want   int64
		logged bool
	}{
		{"no descriptor", nil, 100, false},
		{"applied", func(context.Context) (int64, bool, error) { return 7, true, nil }, 7, false},
		{"not applied", func(context.Context) (int64, bool, error) { return 7, false, nil }, 100, false},
		{"unreadable", func(context.Context) (int64, bool, error) { return 0, false, errBoom }, 100, true},
	} {
		logs.Reset()
		k := New(Deps{
			Redirect:                Redirect{MinSize: 100},
			DownloadRedirectMinSize: tc.read,
			Logger:                  log,
		})
		got := k.minRedirectSize(t.Context())
		logged := strings.Contains(logs.String(), "download redirect: reading the minimum size")
		if got != tc.want || logged != tc.logged {
			t.Errorf(
				"%s: %d, logged %t; want %d, logged %t",
				tc.name,
				got,
				logged,
				tc.want,
				tc.logged,
			)
		}
	}
}

// withoutUnevaluatedPreconditions drops If-Match and If-Unmodified-Since,
// every value of each, from a copy, keeps the conditional headers the
// download evaluates, and leaves the request it was given as it was sent.
// A request without either is returned as it is.
func TestUnitWithoutUnevaluatedPreconditions(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/artifactory/libs-local/a.bin", nil)
	plain.Header.Set("If-None-Match", `"x"`)
	if got := withoutUnevaluatedPreconditions(plain); got != plain {
		t.Fatal("a request without If-Match or If-Unmodified-Since was copied")
	}
	for _, name := range pkgroute.UnevaluatedPreconditions {
		r := httptest.NewRequest(http.MethodGet, "/artifactory/libs-local/a.bin", nil)
		r.Header.Add(name, "a")
		r.Header.Add(name, "b")
		for _, kept := range []string{"If-None-Match", "If-Modified-Since", "If-Range", "Range"} {
			r.Header.Set(kept, kept)
		}
		got := withoutUnevaluatedPreconditions(r)
		if got == r || len(got.Header.Values(name)) != 0 {
			t.Fatalf("%s: %v", name, got.Header)
		}
		for _, kept := range []string{"If-None-Match", "If-Modified-Since", "If-Range", "Range"} {
			if got.Header.Get(kept) != kept {
				t.Fatalf("%s: %s dropped: %v", name, kept, got.Header)
			}
		}
		if len(r.Header.Values(name)) != 2 {
			t.Fatalf("%s: the caller's request changed: %v", name, r.Header)
		}
	}
}

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
		for _, gone := range pkgroute.UnevaluatedPreconditions {
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
// decides alone; "*" and a list on one line do not match; the MD5 matches
// only under a true X-Artifactory-If-None-Match-MD5; otherwise
// If-Modified-Since matches a file unchanged since, to the second.
func TestUnitFileNotModified(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	const md5 = "0123456789abcdef0123456789abcdef"
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
		{"MD5", sha, modified, [][2]string{{"If-None-Match", md5}}, false},
		{
			"MD5 asked for", sha, modified,
			[][2]string{{"If-None-Match", `"` + md5 + `"`}, {"X-Artifactory-If-None-Match-MD5", "True"}},
			true,
		},
		{
			"MD5 asked for with yes", sha, modified,
			[][2]string{{"If-None-Match", md5}, {"X-Artifactory-If-None-Match-MD5", "yes"}},
			true,
		},
		{
			"MD5 not asked for", sha, modified,
			[][2]string{{"If-None-Match", md5}, {"X-Artifactory-If-None-Match-MD5", "false"}},
			false,
		},
		{
			"SHA-1 when the MD5 is asked for", sha, modified,
			[][2]string{{"If-None-Match", sha}, {"X-Artifactory-If-None-Match-MD5", "true"}},
			true,
		},
		{
			"MD5 asked for, none known", "", modified,
			[][2]string{{"If-None-Match", `""`}, {"X-Artifactory-If-None-Match-MD5", "true"}},
			false,
		},
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
		sums := kernel.Checksums{SHA1: c.sha1}
		if c.sha1 != "" {
			sums.MD5 = md5
		}
		if got := fileNotModified(r, sums, c.modified); got != c.want {
			t.Errorf("%s: %t, want %t", c.label, got, c.want)
		}
	}
}

// serveChecksum answers as 7.x's download answers a checksum file: a 304
// with the file's headers, a HEAD with the checksum's type and length and
// the file's headers whatever Range it sends, and a GET with the checksum
// alone, dated now, its Range applied whatever If-Range says, without the
// file's headers or Accept-Ranges. If-Match and If-Unmodified-Since are
// never read.
func TestUnitServeChecksum(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	sums := kernel.Checksums{
		SHA1:   sha,
		SHA256: strings.Repeat("2", 64),
		MD5:    strings.Repeat("5", 32),
	}
	modified := time.Unix(1700000000, 0).UTC()
	serve := func(method string, file kernel.Checksums, at time.Time, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/libs-local/dir/a.bin.sha1", nil)
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		serveChecksum(rec, r, "dir/a.bin.sha1", sha, file, at)
		return rec
	}
	fileHeaders := map[string]string{
		"Last-Modified":          modified.Format(http.TimeFormat),
		"ETag":                   sha,
		"X-Checksum-Sha1":        sha,
		"X-Checksum-Sha256":      sums.SHA256,
		"X-Checksum-Md5":         sums.MD5,
		"X-Artifactory-Filename": "a.bin",
		"Content-Disposition":    `attachment; filename="a.bin"; filename*=UTF-8''a.bin`,
		"Accept-Ranges":          "bytes",
	}
	wantHeaders := func(label string, rec *httptest.ResponseRecorder, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %s %q, want %q", label, k, got, v)
			}
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := serve(method, sums, modified, "If-None-Match", sha)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("%s If-None-Match: %d %q", method, rec.Code, rec.Body)
		}
		wantHeaders(method+" 304", rec, fileHeaders)
		// A file whose checksums and time are not known sends none of them.
		rec = serve(
			method,
			kernel.Checksums{},
			time.Time{},
			"If-Modified-Since",
			modified.Format(http.TimeFormat),
		)
		if rec.Code != http.StatusOK {
			t.Errorf("%s nothing known: %d", method, rec.Code)
		}
		for _, k := range []string{"ETag", "X-Checksum-Sha1", "X-Checksum-Sha256", "X-Checksum-Md5"} {
			if _, sent := rec.Header()[k]; sent {
				t.Errorf("%s nothing known: %s sent", method, k)
			}
		}
	}
	for _, h := range [][]string{
		nil,
		{"If-Match", `"other"`},
		{"If-Unmodified-Since", modified.Add(-time.Hour).Format(http.TimeFormat)},
		{"Range", "bytes=2-5"},
		{"Range", "bytes=1000-2000"},
		{"Range", "bytes=2-5", "If-Range", `"other"`},
	} {
		rec := serve(http.MethodHead, sums, modified, h...)
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 ||
			rec.Header().Get("Content-Range") != "" {
			t.Errorf("HEAD %v: %d %v", h, rec.Code, rec.Header())
		}
		wantHeaders(fmt.Sprintf("HEAD %v", h), rec, fileHeaders)
		wantHeaders(fmt.Sprintf("HEAD %v", h), rec, map[string]string{
			"Content-Type": "application/x-checksum", "Content-Length": "40",
		})
	}
	for _, c := range []struct {
		headers                    []string
		status                     int
		body, length, contentRange string
	}{
		{nil, http.StatusOK, sha, "40", ""},
		{[]string{"If-Match", `"other"`}, http.StatusOK, sha, "40", ""},
		{[]string{"If-None-Match", `"other"`}, http.StatusOK, sha, "40", ""},
		{[]string{"Range", "bytes=2-5"}, http.StatusPartialContent, sha[2:6], "4", "bytes 2-5/40"},
		{[]string{"Range", "bytes=2-5", "If-Range", `"other"`}, http.StatusPartialContent, sha[2:6], "4", "bytes 2-5/40"},
		{[]string{"Range", "bytes=2-5", "If-Range", modified.Add(-time.Hour).Format(http.TimeFormat)}, http.StatusPartialContent, sha[2:6], "4", "bytes 2-5/40"},
		{[]string{"Range", "bytes=1000-2000"}, http.StatusRequestedRangeNotSatisfiable, "", "0", "bytes */40"},
	} {
		start := time.Now().Truncate(time.Second)
		rec := serve(http.MethodGet, sums, modified, c.headers...)
		h := rec.Header()
		dated, err := http.ParseTime(h.Get("Last-Modified"))
		answered := rec.Code == c.status && rec.Body.String() == c.body
		sized := h.Get("Content-Length") == c.length && h.Get("Content-Range") == c.contentRange
		typed := h.Get("Content-Type") == "application/x-checksum"
		if !answered || !sized || !typed || err != nil || dated.Before(start) {
			t.Errorf("GET %v: %d %v %q", c.headers, rec.Code, h, rec.Body)
		}
		for _, k := range []string{
			"ETag", "X-Checksum-Sha1", "X-Artifactory-Filename", "Content-Disposition", "Accept-Ranges",
			"X-Content-Type-Options",
		} {
			if _, sent := h[k]; sent {
				t.Errorf("GET %v: %s sent", c.headers, k)
			}
		}
	}
}

// directServeContent is the call a package type's handler must not make:
// http.ServeContent answers If-Match and If-Unmodified-Since with 412 and a
// HEAD's Range with 206 or 416, which Artifactory's download never does.
const directServeContent = "http.ServeContent("

// directServeContentAllowed are the ecosystem directories whose handlers
// may still call http.ServeContent themselves: the registry drops the
// headers 7.x's Docker API does not act on before it serves a manifest or
// a blob (pullRequest, #1437).
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
