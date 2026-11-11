package helmhttp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func ptr[T any](v T) *T { return &v }

// refusedWith asserts the download policy's 404 in the Artifactory error
// envelope, with message and nothing else.
func refusedWith(t *testing.T, rec *httptest.ResponseRecorder, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 %q: %s", rec.Code, message, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 ||
		body.Errors[0].Status != http.StatusNotFound || body.Errors[0].Message != message {
		t.Fatalf("body %s, want 404 %q", rec.Body, message)
	}
}

func blackedOut(key, path string) string {
	return fmt.Sprintf("The repository '%s' is blacked out and cannot serve artifact '%s:%s'.",
		key, key, path)
}

func rejected(key, path string) string {
	return fmt.Sprintf("The repository '%s' rejected the artifact '%s:%s' due to its "+
		"include/exclude pattern settings.", key, key, path)
}

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.items.Store.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err = f.items.Store.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

// 7.x stores a local repository's index.yaml at its root and reads it
// through the download path, so a blackout or a pattern on index.yaml
// refuses it, after Read permission, while a pattern on one chart refuses
// only that chart and leaves it listed (RTFACT-29572).
func TestAccLocalIndexDownloadPolicy(t *testing.T) {
	f := newFixture(t, true)
	f.deploy("demo-1.0.0.tgz", archive("demo", "1.0.0"))
	index, chart := endpoint+"index.yaml", endpoint+"demo-1.0.0.tgz"
	listed := func() {
		t.Helper()
		rec := f.do(http.MethodGet, index, "reader")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "- demo-1.0.0.tgz") {
			t.Fatalf("index %d, want the chart listed: %s", rec.Code, rec.Body)
		}
	}
	listed()

	f.patch(f.c.Key, func(c *repo.Config) { c.BlackedOut = ptr(true) })
	refusedWith(t, f.do(http.MethodGet, index, "admin"), blackedOut("charts", "index.yaml"))
	refusedWith(t, f.do(http.MethodGet, index, "reader"), blackedOut("charts", "index.yaml"))
	refusedWith(t, f.do(http.MethodGet, chart, "reader"), blackedOut("charts", "demo-1.0.0.tgz"))
	if rec := f.do(http.MethodHead, index, "reader"); rec.Code != http.StatusNotFound {
		t.Fatalf("HEAD index %d, want 404", rec.Code)
	}
	for user, status := range map[string]int{"nobody": 403, "anonymous": 401} {
		if rec := f.do(http.MethodGet, index, user); rec.Code != status ||
			strings.Contains(rec.Body.String(), "blacked out") {
			t.Fatalf("%s: %d %s, want %d before the policy", user, rec.Code, rec.Body, status)
		}
	}

	f.patch(f.c.Key, func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("index.yaml")
	})
	refusedWith(t, f.do(http.MethodGet, index, "reader"), rejected("charts", "index.yaml"))
	if rec := f.do(http.MethodGet, chart, "reader"); rec.Code != http.StatusOK {
		t.Fatalf("chart %d under a pattern on index.yaml: %s", rec.Code, rec.Body)
	}

	f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = ptr("demo-1.0.0.tgz") })
	listed()
	refusedWith(t, f.do(http.MethodGet, chart, "reader"), rejected("charts", "demo-1.0.0.tgz"))
}

// A remote repository caches index.yaml at its root (R1, R4), so its
// blackout and a pattern on index.yaml refuse the index naming that path.
// A chart download looks its URL up in the index on the way, which only the
// blackout refuses, naming the chart. A chart the patterns reject stays in
// the index (RTFACT-29572) and its download answers the patterns' 404, and a
// provenance file is checked at its own path, not its chart's.
func TestAccRemoteDownloadPolicy(t *testing.T) {
	f := newRemoteFixture(t)
	a, _ := f.chart("web", "1.0.0", "visible/web-1.0.0.tgz")
	f.index(a)
	f.up.Set("/charts/visible/web-1.0.0.tgz.prov", fakeupstream.Resource{
		Body: signHelmProvenance(t, fmt.Sprintf(
			"apiVersion: v2\nname: web\nversion: 1.0.0\n...\nfiles:\n  web-1.0.0.tgz: sha256:%s\n",
			a.Digest,
		)),
		Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
	})

	f.configure(func(c *repo.Config) { c.BlackedOut = ptr(true) })
	refusedWith(t, f.get("index.yaml", "admin"), blackedOut("remote", "index.yaml"))
	refusedWith(t, f.get("visible/web-1.0.0.tgz", "reader"),
		blackedOut("remote", "visible/web-1.0.0.tgz"))
	refusedWith(t, f.get("visible/web-1.0.0.tgz.prov", "reader"),
		blackedOut("remote", "visible/web-1.0.0.tgz.prov"))
	expectRemote(t, f.get("index.yaml", "nobody"), http.StatusForbidden)
	expectRemote(t, f.get("index.yaml", "anonymous"), http.StatusUnauthorized)
	if n := f.up.Count("GET", "/charts/index.yaml"); n != 0 {
		t.Fatalf("a blacked-out remote fetched its index %d times", n)
	}

	f.configure(func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("index.yaml")
	})
	refusedWith(t, f.get("index.yaml", "reader"), rejected("remote", "index.yaml"))
	expectRemote(t, f.get("visible/web-1.0.0.tgz", "reader"), http.StatusOK)

	f.configure(func(c *repo.Config) { c.ExcludesPattern = ptr("**/web-1.0.0.tgz") })
	expectRemote(t, f.get("index.yaml", "reader"), http.StatusOK,
		"https://proxy.example"+remoteEndpoint+"visible/web-1.0.0.tgz")
	expectRemote(t, f.get("index.yaml", "partial"), http.StatusOK, "visible/web-1.0.0.tgz")
	refusedWith(t, f.get("visible/web-1.0.0.tgz", "reader"),
		rejected("remote", "visible/web-1.0.0.tgz"))
	expectRemote(t, f.get("visible/web-1.0.0.tgz.prov", "reader"), http.StatusOK)

	f.configure(func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.prov") })
	refusedWith(t, f.get("visible/web-1.0.0.tgz.prov", "reader"),
		rejected("remote", "visible/web-1.0.0.tgz.prov"))
	expectRemote(t, f.get("visible/web-1.0.0.tgz", "reader"), http.StatusOK)
}
