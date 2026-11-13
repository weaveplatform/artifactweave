package itemkit

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	for _, name := range unevaluatedPreconditions {
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
