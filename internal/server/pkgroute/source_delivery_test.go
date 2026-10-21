package pkgroute_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
)

type seekStream struct {
	*strings.Reader
	closed bool
}

func (s *seekStream) Close() error { s.closed = true; return nil }

type refusedStreamWriter struct{ *httptest.ResponseRecorder }

func (refusedStreamWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestSeekableSourceDelivery(t *testing.T) {
	for _, tc := range []struct {
		rangeHeader, etag string
		status            int
		body              string
	}{
		{"bytes=2-4", "", 206, "234"}, {"", `"same"`, 304, ""}, {"bytes=99-", "", 416, "invalid range: failed to overlap\n"},
	} {
		b := &seekStream{Reader: strings.NewReader("0123456789")}
		s := &source.Stream{
			Body:   b,
			Status: 200,
			Representation: source.Representation{
				Size:      10,
				MediaType: "application/octet-stream",
				ETag:      `"same"`,
			},
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Range", tc.rangeHeader)
		r.Header.Set("If-None-Match", tc.etag)
		w := httptest.NewRecorder()
		err := pkgroute.ServeStream(w, r, s)
		if err != nil || w.Code != tc.status || w.Body.String() != tc.body || !b.closed {
			t.Fatalf("response%d %q closed%v error%v", w.Code, w.Body, b.closed, err)
		}
	}
	b := &seekStream{Reader: strings.NewReader("bytes")}
	s := &source.Stream{
		Body:           b,
		Status:         200,
		Representation: source.Representation{Size: 5, MediaType: "application/octet-stream"},
	}
	err := pkgroute.ServeStream(
		refusedStreamWriter{httptest.NewRecorder()},
		httptest.NewRequest(http.MethodGet, "/", nil),
		s,
	)
	if !errors.Is(err, source.ErrUnavailable) || !b.closed {
		t.Fatalf("failed client write %v closed%v", err, b.closed)
	}
}
