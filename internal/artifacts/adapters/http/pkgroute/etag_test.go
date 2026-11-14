package pkgroute_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
