package kittest

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Send sends the route under test a request with method and headers, given
// as name and value pairs, and returns its answer.
type Send func(method string, headers ...string) *httptest.ResponseRecorder

// WantDownloadConditionals fails t unless the route send reaches, which
// serves a document it builds (an index or a metadata file), answers
// conditional requests and ranges as Artifactory's download answers a file
// (itemkit.ServeContent): If-Match and If-Unmodified-Since as the plain
// request, a HEAD with Range with the whole length and no Content-Range,
// and a GET with Range with the part, or 416 past the end, as before. head
// is false for a route that takes only GET. The document must be at least
// 6 bytes long.
func WantDownloadConditionals(t *testing.T, label string, send Send, head bool) {
	t.Helper()
	plain := send(http.MethodGet)
	body := plain.Body.String()
	if plain.Code != http.StatusOK || len(body) < 6 {
		t.Fatalf("%s: GET %d %q", label, plain.Code, plain.Body)
	}
	size := strconv.Itoa(len(body))
	before := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	for _, c := range []struct {
		label   string
		headers []string
	}{
		{"If-Match other", []string{"If-Match", `"other"`}},
		{"If-Unmodified-Since an hour ago", []string{"If-Unmodified-Since", before}},
	} {
		if rec := send(http.MethodGet, c.headers...); rec.Code != http.StatusOK || !sameDocument(rec.Body.String(), body) {
			t.Errorf("%s: GET %s: %d %q, want 200 with the document", label, c.label, rec.Code, rec.Body)
		}
		if head {
			wantWholeHead(t, label+": HEAD "+c.label, send(http.MethodHead, c.headers...), size)
		}
	}
	if head {
		ranges := [][]string{
			{"Range", "bytes=2-5"},
			{"Range", "bytes=" + strconv.Itoa(len(body)+1000) + "-"},
			{"Range", "bytes=2-5", "If-Range", `"other"`},
		}
		if etag := plain.Header().Get("ETag"); etag != "" {
			ranges = append(ranges, []string{"Range", "bytes=2-5", "If-Range", etag})
		}
		for _, h := range ranges {
			wantWholeHead(t, label+": HEAD "+strings.Join(h, " "), send(http.MethodHead, h...), size)
		}
	}
	part := send(http.MethodGet, "Range", "bytes=2-5")
	if part.Code != http.StatusPartialContent || part.Body.String() != body[2:6] ||
		part.Header().Get("Content-Range") != "bytes 2-5/"+size {
		t.Errorf("%s: GET Range: %d %q %q, want 206 with bytes 2-5", label,
			part.Code, part.Header().Get("Content-Range"), part.Body)
	}
	past := send(http.MethodGet, "Range", "bytes="+strconv.Itoa(len(body)+1000)+"-")
	if past.Code != http.StatusRequestedRangeNotSatisfiable ||
		past.Header().Get("Content-Range") != "bytes */"+size {
		t.Errorf("%s: GET Range past the end: %d %q, want 416", label,
			past.Code, past.Header().Get("Content-Range"))
	}
}

// sameDocument reports whether got is the document want, built again: the
// same bytes, or for a document that carries the time it was built, such
// as a virtual Debian Release's Date, the same length and the same first
// line.
func sameDocument(got, want string) bool {
	first := func(s string) string { line, _, _ := strings.Cut(s, "\n"); return line }
	return got == want || (len(got) == len(want) && first(got) == first(want))
}

// wantWholeHead fails t unless rec is a HEAD's 200 for a document of size
// bytes: its whole length, no Content-Range and no body.
func wantWholeHead(t *testing.T, label string, rec *httptest.ResponseRecorder, size string) {
	t.Helper()
	h := rec.Header()
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 ||
		h.Get("Content-Length") != size || h.Get("Content-Range") != "" {
		t.Errorf("%s: %d, Content-Length %q, Content-Range %q, want 200 with Content-Length %s",
			label, rec.Code, h.Get("Content-Length"), h.Get("Content-Range"), size)
	}
}
