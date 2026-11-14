package pkgroute_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
)

type etagCase struct {
	label   string
	headers [][2]string
	status  int
	body    string
}

// serveFileStream serves a stream of path lib/file.bin with no ETag of a
// route's, a file's checksums when withSHA1 is true, and size as its
// length, for c, and returns the answer.
func serveFileStream(
	t *testing.T,
	method string,
	seekable, withSHA1 bool,
	size int64,
	c etagCase,
) *httptest.ResponseRecorder {
	t.Helper()
	s := testStream(seekable, withSHA1)
	s.ETag, s.Path, s.Size = "", "lib/file.bin", size
	r := httptest.NewRequest(method, "/", nil)
	for _, h := range c.headers {
		r.Header.Set(h[0], h[1])
	}
	w := httptest.NewRecorder()
	if err := pkgroute.ServeStream(w, r, s); err != nil {
		t.Fatalf("%s: %v", c.label, err)
	}
	return w
}

// A file stream without an ETag of its route's answers with its SHA-1,
// bare, as its ETag, on a 200, on the 206 an If-Range naming that bare
// SHA-1 admits, and on the 304 an If-None-Match naming it answers, quoted
// or not; another tag matches nothing. 7.161.26's
// RequestResponseHelper.setBasicHeaders sets a downloaded resource's ETag to
// its SHA-1 (#1484).
func TestUnitServeStreamSendsAFilesSHA1AsItsETag(t *testing.T) {
	full := "0123456789"
	for _, seekable := range []bool{true, false} {
		cases := []etagCase{
			{"plain", nil, http.StatusOK, full},
			{"bare match", [][2]string{{"If-None-Match", streamSHA1}}, http.StatusNotModified, ""},
			{
				"quoted match",
				[][2]string{{"If-None-Match", `"` + streamSHA1 + `"`}},
				http.StatusNotModified, "",
			},
			{"other tag", [][2]string{{"If-None-Match", `"0123"`}}, http.StatusOK, full},
		}
		if seekable {
			cases = append(cases,
				etagCase{
					"If-Range bare",
					[][2]string{{"Range", "bytes=0-1"}, {"If-Range", streamSHA1}},
					http.StatusPartialContent, "01",
				},
				etagCase{
					"If-Range other",
					[][2]string{{"Range", "bytes=0-1"}, {"If-Range", `"0123"`}},
					http.StatusOK, full,
				},
			)
		}
		for _, c := range cases {
			w := serveFileStream(t, http.MethodGet, seekable, true, 10, c)
			if w.Code != c.status || w.Body.String() != c.body ||
				w.Header().Get("ETag") != streamSHA1 {
				t.Errorf("%s (seekable %v): %d %q ETag %q, want %d %q and the bare SHA-1",
					c.label, seekable, w.Code, w.Body, w.Header().Get("ETag"), c.status, c.body)
			}
		}
	}
}

// A file stream without a SHA-1, such as a HEAD answered from an upstream's
// HEAD that declares none, sends no ETag and answers If-None-Match with
// 200, "*" and a list included, and If-Modified-Since by its date alone,
// whatever its length. 7.161.26's DownloadServiceImpl.isNotModified matches
// If-None-Match only against the SHA-1 (#1487).
func TestUnitServeStreamMatchesAFileWithoutSHA1ByDateAlone(t *testing.T) {
	after := streamModified.Add(time.Hour).Format(http.TimeFormat)
	before := streamModified.Add(-time.Hour).Format(http.TimeFormat)
	cases := []etagCase{
		{"*", [][2]string{{"If-None-Match", "*"}}, http.StatusOK, ""},
		{"list", [][2]string{{"If-None-Match", `"x", "0123"`}}, http.StatusOK, ""},
		{"after", [][2]string{{"If-Modified-Since", after}}, http.StatusNotModified, ""},
		{"before", [][2]string{{"If-Modified-Since", before}}, http.StatusOK, ""},
	}
	for _, size := range []int64{10, -1} {
		for _, seekable := range []bool{true, false} {
			for _, c := range cases {
				w := serveFileStream(t, http.MethodHead, seekable, false, size, c)
				if w.Code != c.status || w.Header().Get("ETag") != "" {
					t.Errorf("%s (size %d, seekable %v): %d ETag %q, want %d and none", c.label,
						size, seekable, w.Code, w.Header().Get("ETag"), c.status)
				}
			}
		}
	}
}
