package pkgroute_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// A HEAD on a stream answers 200 with the whole length whatever Range and
// If-Range it sends, past the end included, where a GET applies the range
// (#1338).
func TestAccServeStreamHeadIgnoresRange(t *testing.T) {
	for _, tc := range []struct {
		method, rangeHeader, ifRange string
		status                       int
		length, contentRange, body   string
	}{
		{http.MethodHead, "bytes=2-4", "", 200, "10", "", ""},
		{http.MethodHead, "bytes=-3", "", 200, "10", "", ""},
		{http.MethodHead, "bytes=99-", "", 200, "10", "", ""},
		{http.MethodHead, "bytes=2-4", `"same"`, 200, "10", "", ""},
		{http.MethodGet, "bytes=2-4", "", 206, "3", "bytes 2-4/10", "234"},
		{http.MethodGet, "bytes=2-4", `"same"`, 206, "3", "bytes 2-4/10", "234"},
	} {
		s := &source.Stream{
			Body:   &seekStream{Reader: strings.NewReader("0123456789")},
			Status: 200,
			Representation: source.Representation{
				Size: 10, MediaType: "application/octet-stream", ETag: `"same"`,
			},
		}
		r := httptest.NewRequest(tc.method, "/", nil)
		r.Header.Set("Range", tc.rangeHeader)
		if tc.ifRange != "" {
			r.Header.Set("If-Range", tc.ifRange)
		}
		w := httptest.NewRecorder()
		err := pkgroute.ServeStream(w, r, s)
		if err != nil || w.Code != tc.status || w.Body.String() != tc.body ||
			w.Header().Get("Content-Length") != tc.length ||
			w.Header().Get("Content-Range") != tc.contentRange ||
			w.Header().Get("Accept-Ranges") != "bytes" {
			t.Fatalf(
				"%s %s: %d %q %v %v",
				tc.method,
				tc.rangeHeader,
				w.Code,
				w.Body,
				w.Header(),
				err,
			)
		}
		// The caller's request keeps the headers it was sent with.
		if r.Header.Get("Range") != tc.rangeHeader || r.Header.Get("If-Range") != tc.ifRange {
			t.Fatalf("request changed: %v", r.Header)
		}
	}
}

// WithoutRange drops Range and If-Range from a copy and returns a request
// without either as it is.
func TestUnitWithoutRange(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	plain.Header.Set("Accept", "*/*")
	if pkgroute.WithoutRange(plain) != plain {
		t.Fatal("a request without Range was copied")
	}
	for _, h := range [][]string{
		{"Range", "bytes=0-1"},
		{"If-Range", `"same"`},
		{"Range", "bytes=0-1", "If-Range", `"same"`},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "*/*")
		for i := 0; i < len(h); i += 2 {
			r.Header.Set(h[i], h[i+1])
		}
		got := pkgroute.WithoutRange(r)
		if got == r || got.Header.Get("Range") != "" || got.Header.Get("If-Range") != "" ||
			got.Header.Get("Accept") != "*/*" || got.Context() != r.Context() {
			t.Fatalf("%v: %v", h, got.Header)
		}
		if r.Header.Get(h[0]) != h[1] {
			t.Fatalf("%v: the caller's request changed: %v", h, r.Header)
		}
	}
}
