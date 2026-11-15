package huggingfacehttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUnitResolveConditions: an If-None-Match naming the resolve's ETag,
// in any of 7.x's forms, is a 304 the resolve gives; one naming another tag
// leaves the download neither it nor the If-Modified-Since it overrides; an
// If-Range naming the ETag lets the range apply, one naming another tag
// drops the range, and a date or an If-Modified-Since alone stays for the
// download to judge.
func TestUnitResolveConditions(t *testing.T) {
	const etag = "abc"
	request := func(headers ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/f", nil)
		for i := 0; i < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		return r
	}
	for _, tag := range []string{"abc", `"abc"`, `W/"abc"`} {
		if _, unchanged := resolveConditions(request("If-None-Match", tag), etag); !unchanged {
			t.Errorf("If-None-Match %s: not a 304", tag)
		}
	}
	date := "Sun, 15 Nov 2026 10:00:00 GMT"
	for _, tc := range []struct {
		name    string
		headers []string
		kept    []string
		dropped []string
	}{
		{
			"other tag",
			[]string{"If-None-Match", `"x"`, "If-Modified-Since", date},
			nil,
			[]string{"If-None-Match", "If-Modified-Since"},
		},
		{"since alone", []string{"If-Modified-Since", date}, []string{"If-Modified-Since"}, nil},
		{
			"if-range etag",
			[]string{"Range", "bytes=1-", "If-Range", `"abc"`},
			[]string{"Range"},
			[]string{"If-Range"},
		},
		{
			"if-range other",
			[]string{"Range", "bytes=1-", "If-Range", `"x"`},
			nil,
			[]string{"Range", "If-Range"},
		},
		{
			"if-range date",
			[]string{"Range", "bytes=1-", "If-Range", date},
			[]string{"Range", "If-Range"},
			nil,
		},
	} {
		original := request(tc.headers...)
		header, unchanged := resolveConditions(original, etag)
		if unchanged {
			t.Errorf("%s: a 304", tc.name)
		}
		for _, k := range tc.kept {
			if header.Get(k) == "" {
				t.Errorf("%s: %s dropped", tc.name, k)
			}
		}
		for _, k := range tc.dropped {
			if header.Get(k) != "" || original.Header.Get(k) == "" {
				t.Errorf("%s: %s %q, sent %q", tc.name, k, header.Get(k), original.Header.Get(k))
			}
		}
	}
}

// TestUnitSelectedRevalidate: a remote member's fence, then its read
// check, refuse a file before it is answered; a local member's selection
// has neither.
func TestUnitSelectedRevalidate(t *testing.T) {
	fenced, unreadable := errors.New("fenced"), errors.New("unreadable")
	fail := func(err error) func(context.Context) error {
		return func(context.Context) error { return err }
	}
	check := func(err error) func(context.Context, string) error {
		return func(context.Context, string) error { return err }
	}
	for _, tc := range []struct {
		s    selected
		want error
	}{
		{selected{}, nil},
		{selected{fence: fail(nil), check: check(nil)}, nil},
		{selected{fence: fail(fenced), check: check(unreadable)}, fenced},
		{selected{fence: fail(nil), check: check(unreadable)}, unreadable},
	} {
		if err := tc.s.revalidate(t.Context(), "p"); !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v, want %v", tc.s, err, tc.want)
		}
	}
}

// TestUnitResolveWriter: a 200, 206 or 304 goes out with the resolve's ETag
// and without the download's file headers, whether the status is written
// or implied by the body; any other status goes out as it was set.
func TestUnitResolveWriter(t *testing.T) {
	set := func(h http.Header) {
		h.Set("ETag", "sha1")
		h.Set("X-Artifactory-Filename", "f")
		h.Set("X-Checksum-Sha1", "sha1")
		h.Set("Last-Modified", "Sun, 15 Nov 2026 10:00:00 GMT")
		h.Set("Location", "https://storage.example/f")
	}
	for _, tc := range []struct {
		name  string
		write func(w *resolveWriter)
		code  int
		kept  bool
	}{
		{"status", func(w *resolveWriter) { w.WriteHeader(http.StatusPartialContent) }, 206, false},
		{"not modified", func(w *resolveWriter) { w.WriteHeader(http.StatusNotModified) }, 304, false},
		{"body", func(w *resolveWriter) { _, _ = w.Write([]byte("x")) }, 200, false},
		{"reader", func(w *resolveWriter) { _, _ = w.ReadFrom(strings.NewReader("x")) }, 200, false},
		{"redirect", func(w *resolveWriter) { w.WriteHeader(http.StatusFound) }, 302, true},
	} {
		rec := httptest.NewRecorder()
		w := &resolveWriter{ResponseWriter: rec, etag: "sha256"}
		set(w.Header())
		tc.write(w)
		_, _ = w.Write([]byte("y"))
		if rec.Code != tc.code || w.Unwrap() != rec {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.code)
		}
		h := rec.Header()
		if tc.kept != (h.Get("ETag") == "sha1" && h.Get("X-Artifactory-Filename") == "f") ||
			!tc.kept && (h.Get("ETag") != "sha256" || h.Get("X-Checksum-Sha1") != "" ||
				h.Get("Last-Modified") != "" || h.Get("Location") == "") {
			t.Errorf("%s: %v", tc.name, h)
		}
	}
}
