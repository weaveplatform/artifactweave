package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// unsignedBlobs is a blob store whose presigning always fails, as Azure on a
// SAS token or GCS without signBlob does.
type unsignedBlobs struct{ blob.Store }

var errCannotSign = errors.New("cannot sign")

func (unsignedBlobs) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", errCannotSign
}

// redirectWorld is the item world with r.bin deployed to redirect-local.
func redirectWorld(t *testing.T, tune ...func(*Config)) (*itemWorld, string) {
	t.Helper()
	w := newItemWorld(t, false, tune...)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/redirect-local/r.bin", "admin", "redirected"),
		http.StatusCreated,
	)
	return w, "/artifactory/redirect-local/r.bin"
}

func (w *itemWorld) downloads(p string) int64 {
	w.t.Helper()
	stats, err := w.db.Items().Stats(context.Background(), w.item("redirect-local", false, p).ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return stats.DownloadCount
}

// jf rt dl sends a HEAD, following redirects and requiring 200, before it
// downloads any file over 5 MiB. Redirecting that HEAD to a URL presigned
// for GET got 403 from S3 and GCS and failed the download, so HEAD is
// answered here with what the CLI reads.
func TestRedirectAnswersHEADLocallyForJFrogCLISplitDownloads(t *testing.T) {
	w, p := redirectWorld(t)
	rec := w.do("HEAD", p, "admin", "")
	wantStatus(t, rec, http.StatusOK)
	h := rec.Header()
	if h.Get("Location") != "" || h.Get("Content-Length") != strconv.Itoa(len("redirected")) ||
		h.Get(
			"Accept-Ranges",
		) != "bytes" || h.Get("X-Checksum-Sha256") != sha256Hex("redirected") ||
		h.Get("X-Checksum-Sha1") != sha1Hex("redirected") {
		t.Fatalf("HEAD headers %v", h)
	}
	if n := w.downloads("r.bin"); n != 0 {
		t.Fatalf("a HEAD counted as %d downloads", n)
	}
	if rec := w.do("GET", p, "admin", ""); rec.Code != http.StatusFound {
		t.Fatalf("GET %d, want a redirect", rec.Code)
	}
}

func TestRedirectAnswersConditionalRequestsLocally(t *testing.T) {
	w, p := redirectWorld(t)
	etag := `"` + sha1Hex("redirected") + `"`
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	for _, c := range []struct {
		name   string
		header []string
		want   int
	}{
		{"matching If-None-Match", []string{"If-None-Match", etag}, http.StatusNotModified},
		{"If-Modified-Since after the change", []string{"If-Modified-Since", future}, http.StatusNotModified},
		{"failing If-Match", []string{"If-Match", `"other"`}, http.StatusPreconditionFailed},
		{"stale If-None-Match", []string{"If-None-Match", `"other"`}, http.StatusFound},
	} {
		rec := w.do("GET", p, "admin", "", c.header...)
		if rec.Code != c.want {
			t.Fatalf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
		if c.want != http.StatusFound && rec.Header().Get("Location") != "" {
			t.Fatalf("%s: redirected to %s", c.name, rec.Header().Get("Location"))
		}
		if c.want == http.StatusNotModified &&
			(rec.Header().Get("ETag") != etag || rec.Header().Get("Content-Type") != "") {
			t.Fatalf("%s: 304 headers %v", c.name, rec.Header())
		}
	}
	// Only the redirected GET counts.
	if n := w.downloads("r.bin"); n != 1 {
		t.Fatalf("%d downloads counted, want 1", n)
	}
}

func TestRedirectCountsDownloads(t *testing.T) {
	w, p := redirectWorld(t)
	for _, c := range []struct {
		path  string
		rng   string
		count int64
	}{
		{p, "", 1},
		{p, "bytes=0-3", 2},
		{p, "bytes=4-", 2},                    // a later part of a split download
		{p + "?skipUpdateStats=true", "", 2},  // opted out
		{p + "?skipUpdateStats=false", "", 3}, // only "true" opts out
	} {
		var hdr []string
		if c.rng != "" {
			hdr = []string{"Range", c.rng}
		}
		if rec := w.do("GET", c.path, "admin", "", hdr...); rec.Code != http.StatusFound {
			t.Fatalf("GET %s %q: %d", c.path, c.rng, rec.Code)
		}
		if n := w.downloads("r.bin"); n != c.count {
			t.Fatalf("after GET %s %q: %d downloads, want %d", c.path, c.rng, n, c.count)
		}
	}
}

func TestRedirectSigningFailureStreamsAndLogsOnce(t *testing.T) {
	var logs bytes.Buffer
	w, p := redirectWorld(t, func(c *Config) {
		c.Blobs = unsignedBlobs{c.Blobs}
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	for range 3 {
		rec := w.do("GET", p, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		if rec.Body.String() != "redirected" {
			t.Fatalf("streamed %q", rec.Body)
		}
	}
	if n := strings.Count(logs.String(), "signing failed"); n != 1 {
		t.Fatalf("%d signing warnings in a minute, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "level=WARN") ||
		!strings.Contains(logs.String(), "repo=redirect-local") ||
		!strings.Contains(logs.String(), errCannotSign.Error()) {
		t.Fatalf("warning lacks its level, repository or cause:\n%s", logs.String())
	}
	if n := w.downloads("r.bin"); n != 3 {
		t.Fatalf("%d streamed downloads counted, want 3", n)
	}
}

func TestThrottle(t *testing.T) {
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
