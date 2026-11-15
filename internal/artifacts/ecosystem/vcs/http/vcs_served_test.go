package vcshttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// failingWriter refuses the body, as a client gone away does.
type failingWriter struct{ *httptest.ResponseRecorder }

func (w failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

// TestUnitVCSServedAnswersAsTheDownload: a VCS download answers with the
// file's headers, a 304 for its SHA-1 or its time, a range, a 416 with
// Content-Range alone and a HEAD without a range, and counts only a GET
// that delivered all it answered as the file's use.
func TestUnitVCSServedAnswersAsTheDownload(t *testing.T) {
	const body = "archive-bytes"
	modified := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	sums := checksums([]byte(body))
	used := 0
	file := func() vcsServed {
		f := selectedFile("project-v1.tar.gz", []byte(body), modified)
		f.media = "application/x-gzip"
		f.used = func(context.Context) error { used++; return nil }
		return f
	}
	serve := func(f vcsServed, method string, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/a", nil)
		for i := 0; i < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		if err := f.serve(rec, r); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	rec := serve(file(), http.MethodGet)
	want := map[string]string{
		"Content-Type":           "application/x-gzip",
		"Content-Length":         "13",
		"X-Artifactory-Filename": "project-v1.tar.gz",
		"Content-Disposition":    `attachment; filename="project-v1.tar.gz"`,
		"X-Checksum-Sha1":        sums.SHA1,
		"X-Checksum-Sha256":      sums.SHA256,
		"X-Checksum-Md5":         sums.MD5,
		"Etag":                   sums.SHA1,
		"Last-Modified":          modified.Format(http.TimeFormat),
		"Accept-Ranges":          "bytes",
	}
	var got []string
	for k := range rec.Header() {
		got = append(got, k)
		if rec.Header().Get(k) != want[k] {
			t.Errorf("GET %s %q, want %q", k, rec.Header().Get(k), want[k])
		}
	}
	if len(got) != len(want) || rec.Code != http.StatusOK || rec.Body.String() != body ||
		used != 1 {
		t.Fatalf("GET %d %v %q, used %d", rec.Code, got, rec.Body, used)
	}
	for _, tc := range []struct {
		name    string
		method  string
		headers []string
		code    int
		body    string
	}{
		{"bare tag", http.MethodGet, []string{"If-None-Match", sums.SHA1}, 304, ""},
		{"quoted tag", http.MethodGet, []string{"If-None-Match", `"` + sums.SHA1 + `"`}, 304, ""},
		{"since", http.MethodGet, []string{"If-Modified-Since", modified.Format(http.TimeFormat)}, 304, ""},
		{"other tag", http.MethodGet, []string{"If-None-Match", `"x"`}, 200, body},
		{"if-match", http.MethodGet, []string{"If-Match", `"x"`}, 200, body},
		{"range", http.MethodGet, []string{"Range", "bytes=0-6"}, 206, "archive"},
		{"if-range", http.MethodGet, []string{"Range", "bytes=0-6", "If-Range", sums.SHA1}, 206, "archive"},
		{"if-range other", http.MethodGet, []string{"Range", "bytes=0-6", "If-Range", `"x"`}, 200, body},
		{"past the end", http.MethodGet, []string{"Range", "bytes=100-"}, 416, ""},
		{"head range", http.MethodHead, []string{"Range", "bytes=0-6"}, 200, ""},
	} {
		before := used
		rec := serve(file(), tc.method, tc.headers...)
		if rec.Code != tc.code || rec.Body.String() != tc.body {
			t.Errorf("%s: %d %q, want %d %q", tc.name, rec.Code, rec.Body, tc.code, tc.body)
		}
		counted := tc.method == http.MethodGet && (tc.code == 200 || tc.code == 206)
		if (used > before) != counted {
			t.Errorf("%s: use counted %v, want %v", tc.name, used > before, counted)
		}
		switch tc.code {
		case http.StatusNotModified:
			if rec.Header().Get("Etag") != sums.SHA1 || rec.Header().Get("Content-Type") != "" {
				t.Errorf("%s: %v", tc.name, rec.Header())
			}
		case http.StatusRequestedRangeNotSatisfiable:
			if h := rec.Header(); h.Get("Content-Range") != "bytes */13" ||
				h.Get("X-Checksum-Sha1") != "" || h.Get("Etag") != "" {
				t.Errorf("%s: %v", tc.name, h)
			}
		case http.StatusOK:
			if tc.method == http.MethodHead && rec.Header().Get("Content-Length") != "13" {
				t.Errorf("%s: %v", tc.name, rec.Header())
			}
		}
	}
}

// TestUnitVCSServedWithoutSeekOrChecksums: a body that cannot seek is
// answered whole, without Accept-Ranges, and a checksum that is not known
// is left out with the ETag, rather than sent empty; a client's write
// failure is returned and is not the file's use.
func TestUnitVCSServedWithoutSeekOrChecksums(t *testing.T) {
	used := 0
	stream := func(size int64) vcsServed {
		return vcsServed{
			body: io.NopCloser(strings.NewReader("stream")), size: size,
			name: "f.zip", media: "application/zip", sums: kernel.Checksums{SHA256: "abc"},
			used: func(context.Context) error { used++; return nil },
		}
	}
	for _, size := range []int64{6, -1} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/a", strings.NewReader(""))
		r.Header.Set("Range", "bytes=0-1")
		if err := stream(size).serve(rec, r); err != nil {
			t.Fatal(err)
		}
		h := rec.Header()
		if rec.Code != 200 || rec.Body.String() != "stream" || h.Get("Accept-Ranges") != "" ||
			h.Get("Etag") != "" || slices.Contains(
			[]string{h.Get("X-Checksum-Sha1"), h.Get("X-Checksum-Md5")},
			"x",
		) || len(h.Values("X-Checksum-Sha1")) != 0 || h.Get("X-Checksum-Sha256") != "abc" {
			t.Errorf("size %d: %d %q %v", size, rec.Code, rec.Body, h)
		}
		if (h.Get("Content-Length") != "") != (size >= 0) {
			t.Errorf("size %d: Content-Length %q", size, h.Get("Content-Length"))
		}
	}
	head := httptest.NewRecorder()
	if err := stream(6).serve(head, httptest.NewRequest(http.MethodHead, "/a", nil)); err != nil ||
		head.Body.Len() != 0 {
		t.Fatalf("HEAD: %v %q", err, head.Body)
	}
	before := used
	err := stream(
		6,
	).serve(failingWriter{httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/a", nil))
	if err == nil || used != before {
		t.Fatalf("failed write: %v, used %d", err, used-before)
	}
}

// TestUnitKeepRequestHandsTheFileDownloadsTheirRequest: the VCS file
// downloads find their request in the context, every other operation runs
// as it was, and an operation called without one answers a plain GET.
func TestUnitKeepRequestHandsTheFileDownloadsTheirRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/a", nil)
	var seen *http.Request
	next := func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, _ any) (any, error) {
		seen = requestFrom(ctx)
		return nil, nil //nolint:nilnil // a probe of the context alone
	}
	for _, op := range []string{"DownloadVcsTag", "DownloadFileWithinVcsBranch"} {
		seen = nil
		if _, err := KeepRequest(next, op)(t.Context(), nil, r, nil); err != nil || seen != r {
			t.Errorf("%s: %v %v", op, seen, err)
		}
	}
	seen = nil
	if _, err := KeepRequest(next, "GetVcsTags")(t.Context(), nil, r, nil); err != nil ||
		seen == r || seen.Method != http.MethodGet || len(seen.Header) != 0 {
		t.Errorf("GetVcsTags: %v %v", seen, err)
	}
}
