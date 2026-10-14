package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

var errCannotSign = errors.New("cannot sign")

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
		// A signer whose signatures fail, as GCS whose credentials lost
		// signBlob does.
		c.Blobs = signingBlobs{
			Store:  c.Blobs,
			routes: []blob.Via{blob.ViaStorage},
			err:    errCannotSign,
		}
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
		!strings.Contains(logs.String(), "via=storage") ||
		!strings.Contains(logs.String(), errCannotSign.Error()) {
		t.Fatalf("warning lacks its level, repository or cause:\n%s", logs.String())
	}
	if n := w.downloads("r.bin"); n != 3 {
		t.Fatalf("%d streamed downloads counted, want 3", n)
	}
}

// TestRedirectPolicy walks decision 0029's rules: the repository's flags,
// the size threshold, the client's header and the store's routes.
func TestRedirectPolicy(t *testing.T) {
	both := []blob.Via{blob.ViaStorage, blob.ViaCDN}
	storage := []blob.Via{blob.ViaStorage}
	for _, c := range []struct {
		name    string
		repo    string // download-, cdn-, both- or plain-local
		routes  []blob.Via
		choice  string // X-JFrog-Download-Redirect-To
		minSize int64
		want    string // "storage", "cdn" or "" to stream
	}{
		{"no flag streams", "plain-local", both, "", 0, ""},
		{"downloadRedirect to storage", "download-local", storage, "", 0, "storage"},
		{"downloadRedirect prefers the CDN", "download-local", both, "", 0, "cdn"},
		{"cdnRedirect to the CDN", "cdn-local", both, "", 0, "cdn"},
		{"cdnRedirect without a CDN streams", "cdn-local", storage, "", 0, ""},
		{"both flags, no CDN", "both-local", storage, "", 0, "storage"},
		{"a store that signs nothing streams", "both-local", nil, "", 0, ""},
		{"below the threshold streams", "both-local", both, "", 6, ""},
		{"at the threshold redirects", "both-local", both, "", 5, "cdn"},
		{"None streams", "both-local", both, "None", 0, ""},
		{"none is case-insensitive", "both-local", both, "none", 0, ""},
		{"S3 skips the CDN", "both-local", both, "S3", 0, "storage"},
		{"S3 on a cdnRedirect repository streams", "cdn-local", both, "S3", 0, ""},
		{"CF changes nothing", "download-local", both, "CF", 0, "cdn"},
		{"CF without a CDN goes to storage", "download-local", storage, "CF", 0, "storage"},
		{"an unknown value changes nothing", "both-local", both, "Proxy", 0, "cdn"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := policyWorld(t, func(cfg *Config) {
				cfg.Blobs = signingBlobs{Store: cfg.Blobs, routes: c.routes}
				cfg.Redirect.MinSize = c.minSize
			})
			var hdr []string
			if c.choice != "" {
				hdr = []string{redirectChoice, c.choice}
			}
			rec := w.do("GET", "/artifactory/"+c.repo+"/f.bin", "admin", "", hdr...)
			loc := rec.Header().Get("Location")
			switch {
			case c.want == "" && (rec.Code != http.StatusOK || rec.Body.String() != "fifth"):
				t.Fatalf("want a stream, got %d %s", rec.Code, loc)
			case c.want != "" && (rec.Code != http.StatusFound ||
				!strings.HasPrefix(loc, "https://"+c.want+".example/")):
				t.Fatalf("want a redirect via %s, got %d %q", c.want, rec.Code, loc)
			}
		})
	}
}

// policyWorld is the item world with f.bin ("fifth", five bytes) in a
// repository for each combination of the two flags.
func policyWorld(t *testing.T, tune ...func(*Config)) *itemWorld {
	t.Helper()
	w := newItemWorld(t, false, tune...)
	for _, c := range []repo.Config{
		{Key: "plain-local", Class: repo.Local},
		{Key: "download-local", Class: repo.Local, DownloadRedirect: repo.Flex(true)},
		{Key: "cdn-local", Class: repo.Local, CDNRedirect: repo.Flex(true)},
		{Key: "both-local", Class: repo.Local, DownloadRedirect: repo.Flex(true), CDNRedirect: repo.Flex(true)},
	} {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		wantStatus(
			t,
			w.do("PUT", "/artifactory/"+c.Key+"/f.bin", "admin", "fifth"),
			http.StatusCreated,
		)
	}
	return w
}

// The 302 has no body, so it drops what describes one; the signed URL
// carries the name, media type and lifetime instead.
func TestRedirectResponse(t *testing.T) {
	w, p := redirectWorld(t, func(c *Config) { c.Redirect.TTL = 90 * time.Second })
	rec := w.do("GET", p, "admin", "")
	wantStatus(t, rec, http.StatusFound)
	h := rec.Header()
	for _, k := range []string{"Content-Type", "Content-Disposition", "ETag"} {
		if v := h.Get(k); v != "" {
			t.Errorf("302 carries %s: %q", k, v)
		}
	}
	if h.Get("Cache-Control") != "private, no-store" ||
		h.Get("X-Checksum-Sha256") != sha256Hex("redirected") ||
		h.Get("X-Checksum-Sha1") != sha1Hex("redirected") ||
		h.Get("X-Artifactory-Filename") != "r.bin" || rec.Body.Len() != 0 {
		t.Fatalf("302 headers %v body %q", h, rec.Body)
	}
	u, err := url.Parse(h.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); q.Get("name") != "r.bin" || q.Get("ttl") != "1m30s" || q.Get("type") == "" {
		t.Fatalf("signing options %v", q)
	}
	// Without a TTL the default applies.
	w, p = redirectWorld(t)
	u, err = url.Parse(w.do("GET", p, "admin", "").Header().Get("Location"))
	if err != nil || u.Query().Get("ttl") != DefaultRedirectTTL.String() {
		t.Fatalf("default ttl: %v %v", u, err)
	}
	// A streamed download keeps its headers.
	rec = w.do("HEAD", p, "admin", "")
	if rec.Header().Get("Content-Disposition") != blob.Disposition("r.bin") ||
		rec.Header().Get("ETag") == "" {
		t.Fatalf("streamed headers %v", rec.Header())
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
