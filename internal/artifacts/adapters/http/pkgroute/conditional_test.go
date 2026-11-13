package pkgroute_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const (
	streamSHA1 = "0123456789abcdef0123456789abcdef01234567"
	streamMD5  = "0123456789abcdef0123456789abcdef"
)

var streamModified = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// plainStream is a stream body that cannot seek, as an upstream's is.
type plainStream struct{ io.Reader }

func (plainStream) Close() error { return nil }

// testStream is a 200 stream of "0123456789" with ETag 0123, seekable or
// not, and a file's checksums when file is true.
func testStream(seekable, file bool) *source.Stream {
	var body io.ReadCloser = plainStream{strings.NewReader("0123456789")}
	if seekable {
		body = &seekStream{Reader: strings.NewReader("0123456789")}
	}
	s := &source.Stream{Body: body, Status: http.StatusOK, Representation: source.Representation{
		Size: 10, MediaType: "application/octet-stream", ETag: "0123", Modified: streamModified,
	}}
	if file {
		s.Checksums = kernel.Checksums{SHA1: streamSHA1, MD5: streamMD5}
	}
	return s
}

// A stream answers If-Match and If-Unmodified-Since as its cached copy
// does, never with 412, whatever tag or date they carry: 7.x's download
// reads neither (DownloadServiceImpl.isNotModified, 7.161.26; RTFACT-17259
// closed without them). Seekable or not, a file or a document, GET or HEAD,
// it answers 200 with the whole body or length (#1468).
func TestUnitServeStreamIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	for _, header := range [][2]string{
		{"If-Match", `"other"`},
		{"If-Match", "0123"},
		{"If-Match", `"0123"`},
		{"If-Unmodified-Since", streamModified.Add(-time.Hour).Format(http.TimeFormat)},
	} {
		for _, seekable := range []bool{true, false} {
			for _, file := range []bool{true, false} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					r := httptest.NewRequest(method, "/", nil)
					r.Header.Set(header[0], header[1])
					w := httptest.NewRecorder()
					err := pkgroute.ServeStream(w, r, testStream(seekable, file))
					body := "0123456789"
					if method == http.MethodHead {
						body = ""
					}
					if err != nil || w.Code != http.StatusOK || w.Body.String() != body ||
						w.Header().Get("Content-Length") != "10" {
						t.Errorf("%s %s: %s (seekable %v, file %v): %d %q %v %v", method, header[0], header[1],
							seekable, file, w.Code, w.Body, w.Header(), err)
					}
					if r.Header.Get(header[0]) != header[1] {
						t.Fatalf("request changed: %v", r.Header)
					}
				}
			}
		}
	}
}
