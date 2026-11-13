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

// A stream with a SHA-1, a file, answers If-None-Match as 7.x's download
// does (ArtifactoryRequestBase.isNoneMatch and parseETagValue, 7.161.26):
// one header line at a time, less a W/ prefix and its quotes, equal to its
// SHA-1, to the ETag it sends, or to its MD5 when the request asks with
// X-Artifactory-If-None-Match-MD5. Neither "*" nor a list of tags on one
// line matches. Its 416 carries Content-Range alone and no body
// (RequestResponseHelper.sendBodyResponse) (#1469).
func TestUnitServeStreamAnswersAFileAsTheDownload(t *testing.T) {
	since := streamModified.Add(time.Minute).Format(http.TimeFormat)
	for _, tc := range []struct {
		name    string
		headers [][2]string
		status  int
	}{
		{"star", [][2]string{{"If-None-Match", "*"}}, http.StatusOK},
		{"a list naming the SHA-1", [][2]string{{"If-None-Match", `"other", ` + streamSHA1}}, http.StatusOK},
		{"the SHA-1 on a second line", [][2]string{
			{"If-None-Match", `"other"`}, {"If-None-Match", streamSHA1},
		}, http.StatusNotModified},
		{"the SHA-1, weak and quoted", [][2]string{{"If-None-Match", `W/"` + streamSHA1 + `"`}}, http.StatusNotModified},
		{"the ETag sent", [][2]string{{"If-None-Match", "0123"}}, http.StatusNotModified},
		{"the MD5, asked for", [][2]string{
			{"If-None-Match", streamMD5}, {"X-Artifactory-If-None-Match-MD5", "true"},
		}, http.StatusNotModified},
		{"the MD5, not asked for", [][2]string{{"If-None-Match", streamMD5}}, http.StatusOK},
		{"another tag", [][2]string{{"If-None-Match", `"other"`}}, http.StatusOK},
		{"unchanged since", [][2]string{{"If-Modified-Since", since}}, http.StatusNotModified},
		// If-None-Match decides alone.
		{"another tag, unchanged since", [][2]string{
			{"If-None-Match", `"other"`}, {"If-Modified-Since", since},
		}, http.StatusOK},
	} {
		for _, seekable := range []bool{true, false} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				r := httptest.NewRequest(method, "/", nil)
				for _, h := range tc.headers {
					r.Header.Add(h[0], h[1])
				}
				w := httptest.NewRecorder()
				err := pkgroute.ServeStream(w, r, testStream(seekable, true))
				body := "0123456789"
				if method == http.MethodHead || tc.status == http.StatusNotModified {
					body = ""
				}
				if err != nil || w.Code != tc.status || w.Body.String() != body {
					t.Errorf("%s %s (seekable %v): %d %q %v %v", method, tc.name, seekable,
						w.Code, w.Body, w.Header(), err)
				}
				if tc.status == http.StatusNotModified && (w.Header().Get("ETag") != "0123" ||
					w.Header().Get("X-Checksum-Sha1") != streamSHA1 ||
					w.Header().Get("Content-Type") != "" || w.Header().Get("Content-Length") != "" ||
					w.Header().Get("Last-Modified") != "") {
					t.Errorf("%s %s (seekable %v): 304 headers %v", method, tc.name, seekable, w.Header())
				}
			}
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Range", "bytes=99-")
	w := httptest.NewRecorder()
	if err := pkgroute.ServeStream(w, r, testStream(true, true)); err != nil ||
		w.Code != http.StatusRequestedRangeNotSatisfiable || w.Body.String() != "" {
		t.Fatalf("416: %d %q %v", w.Code, w.Body, err)
	}
	want := http.Header{"Content-Range": {"bytes */10"}, "Content-Length": {"0"}}
	if len(w.Header()) != len(want) || w.Header().Get("Content-Range") != "bytes */10" ||
		w.Header().Get("Content-Length") != "0" {
		t.Fatalf("416 headers %v, want %v", w.Header(), want)
	}
}

// A document a protocol builds per request carries no SHA-1, and keeps
// http.ServeContent's answers, which no 7.x source shows for those routes:
// "*" and a list naming its ETag answer 304, and a 416 carries
// http.Error's text.
func TestUnitServeStreamKeepsADocumentsAnswers(t *testing.T) {
	for _, tag := range []string{"*", `"other", "0123"`} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("If-None-Match", tag)
		w := httptest.NewRecorder()
		s := testStream(true, false)
		s.ETag = `"0123"`
		if err := pkgroute.ServeStream(w, r, s); err != nil || w.Code != http.StatusNotModified {
			t.Errorf("%s: %d %q %v", tag, w.Code, w.Body, err)
		}
	}
}

func TestUnitFileNotModified(t *testing.T) {
	modified := streamModified.Add(500 * time.Millisecond)
	for _, tc := range []struct {
		name    string
		headers [][2]string
		md5     string
		want    bool
	}{
		{"nothing", nil, streamMD5, false},
		{"the SHA-1", [][2]string{{"If-None-Match", " " + streamSHA1 + " "}}, streamMD5, true},
		{"W/ inside quotes", [][2]string{{"If-None-Match", `W/"W/` + streamSHA1 + `"`}}, streamMD5, true},
		{"an empty line", [][2]string{{"If-None-Match", `""`}}, "", false},
		{"the MD5 with y", [][2]string{
			{"If-None-Match", streamMD5}, {"X-Artifactory-If-None-Match-MD5", "Y"},
		}, streamMD5, true},
		{"the MD5 with false", [][2]string{
			{"If-None-Match", streamMD5}, {"X-Artifactory-If-None-Match-MD5", "false"},
		}, streamMD5, false},
		{"only the first flag counts", [][2]string{
			{"If-None-Match", streamMD5}, {"X-Artifactory-If-None-Match-MD5", "no"},
			{"X-Artifactory-If-None-Match-MD5", "true"},
		}, streamMD5, false},
		{"since the same second", [][2]string{
			{"If-Modified-Since", streamModified.Format(http.TimeFormat)},
		}, "", true},
		{"since before", [][2]string{
			{"If-Modified-Since", streamModified.Add(-time.Second).Format(http.TimeFormat)},
		}, "", false},
		{"a date that does not parse", [][2]string{{"If-Modified-Since", "yesterday"}}, "", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, h := range tc.headers {
			r.Header.Add(h[0], h[1])
		}
		if got := pkgroute.FileNotModified(r, []string{streamSHA1, ""}, tc.md5, modified); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if pkgroute.FileNotModified(httptest.NewRequest(http.MethodGet, "/", nil), nil, "", time.Time{}) {
		t.Error("no validators matched")
	}
}

// RangeRefusalWriter copies a body through io.Copy's fast path unless it
// has sent a 416, whose body it drops, and unwraps for
// http.ResponseController.
func TestUnitRangeRefusalWriterCopies(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &pkgroute.RangeRefusalWriter{ResponseWriter: rec}
	if w.Unwrap() != rec {
		t.Fatal("Unwrap is not the wrapped writer")
	}
	if n, err := w.ReadFrom(strings.NewReader("body")); n != 4 || err != nil || rec.Body.String() != "body" {
		t.Fatalf("copied %d %v %q", n, err, rec.Body)
	}
	rec = httptest.NewRecorder()
	w = &pkgroute.RangeRefusalWriter{ResponseWriter: rec}
	w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	if n, err := w.ReadFrom(strings.NewReader("body")); n != 4 || err != nil || rec.Body.Len() != 0 {
		t.Fatalf("416 copied %d %v %q", n, err, rec.Body)
	}
}
